package auth

import (
	"encoding/base64"
	"errors"
	"math"
	"strconv"
	"strings"
)

// Pure-Go ISO/IEC 18004 QR Code Generator (Byte Mode, ECC Level L, Versions 1..10).
// Designed so TOTP otpauth:// URIs render offline into crisp SVG data URLs without external dependencies or network leaks.

type qrVersionSpec struct {
	version       int
	totalData     int
	ecPerBlock    int
	blocksGroup1  int
	dataPerBlock1 int
	blocksGroup2  int
	dataPerBlock2 int
	alignCoords   []int
}

var qrSpecsLevelL = []qrVersionSpec{
	{1, 19, 7, 1, 19, 0, 0, nil},
	{2, 34, 10, 1, 34, 0, 0, []int{6, 18}},
	{3, 55, 15, 1, 55, 0, 0, []int{6, 22}},
	{4, 80, 20, 1, 80, 0, 0, []int{6, 26}},
	{5, 108, 26, 1, 108, 0, 0, []int{6, 30}},
	{6, 136, 18, 2, 68, 0, 0, []int{6, 34}},
	{7, 156, 20, 2, 78, 0, 0, []int{6, 22, 38}},
	{8, 194, 24, 2, 97, 0, 0, []int{6, 24, 42}},
	{9, 232, 30, 2, 116, 0, 0, []int{6, 26, 46}},
	{10, 274, 18, 2, 68, 2, 69, []int{6, 28, 50}},
}

// GenerateQRCodeDataURL encodes text into an offline SVG data URI (`data:image/svg+xml;base64,...`).
func GenerateQRCodeDataURL(text string) (string, error) {
	svg, err := GenerateQRCodeSVG(text, 4)
	if err != nil {
		return "", err
	}
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg)), nil
}

// GenerateQRCodeSVG encodes text into a compact <svg> string using path run-length encoding.
func GenerateQRCodeSVG(text string, border int) (string, error) {
	modules, err := encodeQRMatrix([]byte(text))
	if err != nil {
		return "", err
	}
	if border < 2 {
		border = 4
	}
	size := len(modules)
	dim := size + border*2

	var sb strings.Builder
	sb.Grow(size * size * 3)
	sb.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 `)
	sb.WriteString(strconv.Itoa(dim))
	sb.WriteByte(' ')
	sb.WriteString(strconv.Itoa(dim))
	sb.WriteString(`" shape-rendering="crispEdges"><rect width="100%" height="100%" fill="#ffffff"/><path fill="#000000" d="`)

	for r := 0; r < size; r++ {
		c := 0
		for c < size {
			if !modules[r][c] {
				c++
				continue
			}
			run := 1
			for c+run < size && modules[r][c+run] {
				run++
			}
			sb.WriteByte('M')
			sb.WriteString(strconv.Itoa(c + border))
			sb.WriteByte(',')
			sb.WriteString(strconv.Itoa(r + border))
			sb.WriteByte('h')
			sb.WriteString(strconv.Itoa(run))
			sb.WriteString("v1h-")
			sb.WriteString(strconv.Itoa(run))
			sb.WriteByte('z')
			c += run
		}
	}
	sb.WriteString(`"/></svg>`)
	return sb.String(), nil
}

func encodeQRMatrix(payload []byte) ([][]bool, error) {
	var spec *qrVersionSpec
	for i := range qrSpecsLevelL {
		s := &qrSpecsLevelL[i]
		countBits := 8
		if s.version >= 10 {
			countBits = 16
		}
		availBits := s.totalData*8 - 4 - countBits
		if len(payload)*8 <= availBits {
			spec = s
			break
		}
	}
	if spec == nil {
		return nil, errors.New("QR payload exceeds Version 10 capacity")
	}

	dataCodewords := buildDataCodewords(payload, spec)
	allCodewords := interleaveWithECC(dataCodewords, spec)

	size := spec.version*4 + 17
	modules := make([][]bool, size)
	isFunc := make([][]bool, size)
	for i := 0; i < size; i++ {
		modules[i] = make([]bool, size)
		isFunc[i] = make([]bool, size)
	}

	drawFunctionPatterns(modules, isFunc, spec)
	drawCodewords(modules, isFunc, allCodewords)

	// Evaluate all 8 masks and pick lowest ISO penalty score
	bestMask := 0
	bestScore := math.MaxInt32
	for mask := 0; mask < 8; mask++ {
		applyMask(modules, isFunc, mask)
		drawFormatBits(modules, isFunc, mask)
		score := computePenalty(modules)
		if score < bestScore {
			bestScore = score
			bestMask = mask
		}
		applyMask(modules, isFunc, mask) // revert XOR
	}

	applyMask(modules, isFunc, bestMask)
	drawFormatBits(modules, isFunc, bestMask)
	return modules, nil
}

func buildDataCodewords(payload []byte, spec *qrVersionSpec) []byte {
	capBits := spec.totalData * 8
	bits := make([]uint8, 0, capBits)
	appendBits := func(val int, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, uint8((val>>i)&1))
		}
	}

	// Byte mode indicator 0100
	appendBits(0b0100, 4)
	if spec.version >= 10 {
		appendBits(len(payload), 16)
	} else {
		appendBits(len(payload), 8)
	}
	for _, b := range payload {
		appendBits(int(b), 8)
	}
	// Terminator (up to 4 zeros)
	for i := 0; i < 4 && len(bits) < capBits; i++ {
		bits = append(bits, 0)
	}
	// Pad to byte boundary
	for len(bits)%8 != 0 {
		bits = append(bits, 0)
	}

	out := make([]byte, 0, spec.totalData)
	for i := 0; i < len(bits); i += 8 {
		var v byte
		for j := 0; j < 8; j++ {
			v = (v << 1) | bits[i+j]
		}
		out = append(out, v)
	}
	padBytes := [2]byte{0xEC, 0x11}
	for idx := 0; len(out) < spec.totalData; idx++ {
		out = append(out, padBytes[idx&1])
	}
	return out
}

func interleaveWithECC(data []byte, spec *qrVersionSpec) []byte {
	totalBlocks := spec.blocksGroup1 + spec.blocksGroup2
	dataBlocks := make([][]byte, 0, totalBlocks)
	ecBlocks := make([][]byte, 0, totalBlocks)
	gen := rsGeneratorPoly(spec.ecPerBlock)

	offset := 0
	for i := 0; i < spec.blocksGroup1; i++ {
		blk := data[offset : offset+spec.dataPerBlock1]
		offset += spec.dataPerBlock1
		dataBlocks = append(dataBlocks, blk)
		ecBlocks = append(ecBlocks, rsComputeRemainder(blk, gen))
	}
	for i := 0; i < spec.blocksGroup2; i++ {
		blk := data[offset : offset+spec.dataPerBlock2]
		offset += spec.dataPerBlock2
		dataBlocks = append(dataBlocks, blk)
		ecBlocks = append(ecBlocks, rsComputeRemainder(blk, gen))
	}

	maxDataLen := spec.dataPerBlock1
	if spec.dataPerBlock2 > maxDataLen {
		maxDataLen = spec.dataPerBlock2
	}

	res := make([]byte, 0, spec.totalData+totalBlocks*spec.ecPerBlock)
	for col := 0; col < maxDataLen; col++ {
		for _, blk := range dataBlocks {
			if col < len(blk) {
				res = append(res, blk[col])
			}
		}
	}
	for col := 0; col < spec.ecPerBlock; col++ {
		for _, ec := range ecBlocks {
			res = append(res, ec[col])
		}
	}
	return res
}

func gfMul(x, y byte) byte {
	var z byte
	for i := 7; i >= 0; i-- {
		z = (z << 1) ^ ((z >> 7) * 0x1D)
		if ((y >> i) & 1) != 0 {
			z ^= x
		}
	}
	return z
}

func rsGeneratorPoly(degree int) []byte {
	poly := make([]byte, degree)
	poly[degree-1] = 1
	root := byte(1)
	for i := 0; i < degree; i++ {
		for j := 0; j < degree; j++ {
			poly[j] = gfMul(poly[j], root)
			if j+1 < degree {
				poly[j] ^= poly[j+1]
			}
		}
		root = gfMul(root, 0x02)
	}
	return poly
}

func rsComputeRemainder(data, gen []byte) []byte {
	rem := make([]byte, len(gen))
	for _, b := range data {
		factor := b ^ rem[0]
		copy(rem, rem[1:])
		rem[len(rem)-1] = 0
		for j, coef := range gen {
			rem[j] ^= gfMul(coef, factor)
		}
	}
	return rem
}

func setFuncModule(modules, isFunc [][]bool, r, c int, dark bool) {
	modules[r][c] = dark
	isFunc[r][c] = true
}

func drawFunctionPatterns(modules, isFunc [][]bool, spec *qrVersionSpec) {
	size := len(modules)
	// Timing patterns
	for i := 0; i < size; i++ {
		setFuncModule(modules, isFunc, 6, i, i%2 == 0)
		setFuncModule(modules, isFunc, i, 6, i%2 == 0)
	}

	// 3 Finder patterns + white separators
	drawFinder := func(centerR, centerC int) {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				r, c := centerR+dy, centerC+dx
				if r < 0 || r >= size || c < 0 || c >= size {
					continue
				}
				dist := absInt(dx)
				if absInt(dy) > dist {
					dist = absInt(dy)
				}
				dark := dist != 2 && dist != 4
				setFuncModule(modules, isFunc, r, c, dark)
			}
		}
	}
	drawFinder(3, 3)
	drawFinder(3, size-4)
	drawFinder(size-4, 3)

	// Alignment patterns
	coords := spec.alignCoords
	for i := 0; i < len(coords); i++ {
		for j := 0; j < len(coords); j++ {
			if (i == 0 && j == 0) || (i == 0 && j == len(coords)-1) || (i == len(coords)-1 && j == 0) {
				continue
			}
			cr, cc := coords[i], coords[j]
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					dist := absInt(dx)
					if absInt(dy) > dist {
						dist = absInt(dy)
					}
					setFuncModule(modules, isFunc, cr+dy, cc+dx, dist != 1)
				}
			}
		}
	}

	// Reserve format info strips & always-dark module
	drawFormatBits(modules, isFunc, 0)

	// Version info for Version >= 7
	if spec.version >= 7 {
		rem := spec.version
		for i := 0; i < 12; i++ {
			rem = (rem << 1) ^ ((rem >> 11) * 0x1F25)
		}
		bits := (spec.version << 12) | rem
		for i := 0; i < 18; i++ {
			dark := ((bits >> i) & 1) != 0
			a := size - 11 + (i % 3)
			b := i / 3
			setFuncModule(modules, isFunc, a, b, dark)
			setFuncModule(modules, isFunc, b, a, dark)
		}
	}
}

func drawFormatBits(modules, isFunc [][]bool, mask int) {
	size := len(modules)
	// ECC Level L indicator = 0b01
	data := (0b01 << 3) | mask
	rem := data
	for i := 0; i < 10; i++ {
		rem = (rem << 1) ^ ((rem >> 9) * 0x537)
	}
	bits := ((data << 10) | rem) ^ 0x5412

	// Copy 1: around top-left finder
	for i := 0; i <= 5; i++ {
		setFuncModule(modules, isFunc, i, 8, ((bits>>i)&1) != 0)
	}
	setFuncModule(modules, isFunc, 7, 8, ((bits>>6)&1) != 0)
	setFuncModule(modules, isFunc, 8, 8, ((bits>>7)&1) != 0)
	setFuncModule(modules, isFunc, 8, 7, ((bits>>8)&1) != 0)
	for i := 9; i < 15; i++ {
		setFuncModule(modules, isFunc, 8, 14-i, ((bits>>i)&1) != 0)
	}

	// Copy 2: top-right & bottom-left + always-dark module
	for i := 0; i < 8; i++ {
		setFuncModule(modules, isFunc, 8, size-1-i, ((bits>>i)&1) != 0)
	}
	for i := 8; i < 15; i++ {
		setFuncModule(modules, isFunc, size-15+i, 8, ((bits>>i)&1) != 0)
	}
	setFuncModule(modules, isFunc, size-8, 8, true)
}

func drawCodewords(modules, isFunc [][]bool, codewords []byte) {
	size := len(modules)
	bitIdx := 0
	totalBits := len(codewords) * 8

	upward := true
	for right := size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < size; vert++ {
			r := vert
			if upward {
				r = size - 1 - vert
			}
			for j := 0; j < 2; j++ {
				c := right - j
				if !isFunc[r][c] {
					dark := false
					if bitIdx < totalBits {
						dark = ((codewords[bitIdx>>3] >> (7 - (bitIdx & 7))) & 1) != 0
						bitIdx++
					}
					modules[r][c] = dark
				}
			}
		}
		upward = !upward
	}
}

func applyMask(modules, isFunc [][]bool, mask int) {
	size := len(modules)
	for r := 0; r < size; r++ {
		for c := 0; c < size; c++ {
			if isFunc[r][c] {
				continue
			}
			var invert bool
			switch mask {
			case 0:
				invert = (r+c)%2 == 0
			case 1:
				invert = r%2 == 0
			case 2:
				invert = c%3 == 0
			case 3:
				invert = (r+c)%3 == 0
			case 4:
				invert = (r/2+c/3)%2 == 0
			case 5:
				invert = (r*c)%2+(r*c)%3 == 0
			case 6:
				invert = ((r*c)%2+(r*c)%3)%2 == 0
			case 7:
				invert = ((r+c)%2+(r*c)%3)%2 == 0
			}
			if invert {
				modules[r][c] = !modules[r][c]
			}
		}
	}
}

func computePenalty(modules [][]bool) int {
	size := len(modules)
	score := 0

	// Rule 1: runs of >= 5 identical modules in rows and cols
	for r := 0; r < size; r++ {
		runColor := modules[r][0]
		runLen := 1
		for c := 1; c < size; c++ {
			if modules[r][c] == runColor {
				runLen++
				if runLen == 5 {
					score += 3
				} else if runLen > 5 {
					score++
				}
			} else {
				runColor = modules[r][c]
				runLen = 1
			}
		}
	}
	for c := 0; c < size; c++ {
		runColor := modules[0][c]
		runLen := 1
		for r := 1; r < size; r++ {
			if modules[r][c] == runColor {
				runLen++
				if runLen == 5 {
					score += 3
				} else if runLen > 5 {
					score++
				}
			} else {
				runColor = modules[r][c]
				runLen = 1
			}
		}
	}

	// Rule 2: 2x2 blocks of identical color
	darkCount := 0
	for r := 0; r < size; r++ {
		for c := 0; c < size; c++ {
			if modules[r][c] {
				darkCount++
			}
			if r < size-1 && c < size-1 {
				v := modules[r][c]
				if modules[r][c+1] == v && modules[r+1][c] == v && modules[r+1][c+1] == v {
					score += 3
				}
			}
		}
	}

	total := size * size
	k := (absInt(darkCount*20-total*10) + total - 1) / total
	if k > 0 {
		score += (k - 1) * 10
	}
	return score
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
