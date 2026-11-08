package main

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sijms/go-ora/v2/converters"
)

type converterSpec struct {
	Cases        []int
	DynamicLang  bool
	FixedLangID  int
	CharWidth    int
	EReplace     int
	DReplace     int
	DBuffer      []int
	DBuffer2     []int
	EBuffer      map[int]int
	Leading      map[int]int
}

var caseTokenRe = regexp.MustCompile(`(?m)^\s*case\s+([^:]+):`)

func extractAllLangIDs(src string) ([]int, error) {
	seen := make(map[int]struct{}, 256)
	var ids []int
	for _, m := range caseTokenRe.FindAllStringSubmatch(src, -1) {
		for _, part := range strings.Split(m[1], ",") {
			tok := strings.TrimSpace(part)
			if tok == "" {
				continue
			}
			v, err := strconv.ParseInt(tok, 0, 64)
			if err != nil {
				return nil, fmt.Errorf("parse case %q: %w", tok, err)
			}
			id := int(v)
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	sort.Ints(ids)
	return ids, nil
}

func inspectConverter(id int) (*converterSpec, bool) {
	c := converters.NewStringConverter(id)
	if c == nil {
		return nil, false
	}
	rv := reflect.ValueOf(c)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}

	readSlice := func(name string) []int {
		f := rv.FieldByName(name)
		if !f.IsValid() || f.IsNil() || f.Len() == 0 {
			return nil
		}
		out := make([]int, f.Len())
		for i := 0; i < f.Len(); i++ {
			out[i] = int(f.Index(i).Int())
		}
		return out
	}

	readMap := func(name string) map[int]int {
		f := rv.FieldByName(name)
		if !f.IsValid() || f.IsNil() || f.Len() == 0 {
			return nil
		}
		out := make(map[int]int, f.Len())
		iter := f.MapRange()
		for iter.Next() {
			out[int(iter.Key().Int())] = int(iter.Value().Int())
		}
		return out
	}

	return &converterSpec{
		Cases:       []int{id},
		FixedLangID: int(rv.FieldByName("LangID").Int()),
		CharWidth:   int(rv.FieldByName("CharWidth").Int()),
		EReplace:    int(rv.FieldByName("eReplace").Int()),
		DReplace:    int(rv.FieldByName("dReplace").Int()),
		DBuffer:     readSlice("dBuffer"),
		DBuffer2:    readSlice("dBuffer2"),
		EBuffer:     readMap("eBuffer"),
		Leading:     readMap("leading"),
	}, true
}

func deriveBaseEBuffer(dBuffer, dBuffer2 []int) map[int]int {
	if len(dBuffer) == 0 {
		return nil
	}
	derived := make(map[int]int, len(dBuffer))
	if len(dBuffer2) == 0 {
		for b, u := range dBuffer {
			if u != 0xFFFF {
				if _, exists := derived[u]; !exists {
					derived[u] = b
				}
			}
		}
		return derived
	}
	maxIdx1 := 256
	if len(dBuffer) < maxIdx1 {
		maxIdx1 = len(dBuffer)
	}
	for idx1 := 0; idx1 < maxIdx1; idx1++ {
		base := dBuffer[idx1]
		if base == 0xFFFF {
			continue
		}
		for idx2 := 0; idx2 < 256; idx2++ {
			if base+idx2 >= len(dBuffer2) {
				break
			}
			u := dBuffer2[base+idx2]
			if u != 0xFFFF {
				ch := idx2
				if idx1 != 0 {
					ch = (idx1 << 8) | idx2
				}
				if _, exists := derived[u]; !exists {
					derived[u] = ch
				}
			}
		}
	}
	return derived
}

func appendSvarint(dst []byte, v int) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutVarint(tmp[:], int64(v))
	return append(dst, tmp[:n]...)
}

type kvPair struct {
	k int
	v int
}

func packConverterSpecs(specs []*converterSpec) ([]byte, error) {
	var colMeta, colSlices, colKeys, colVals []byte

	for _, s := range specs {
		colMeta = appendSvarint(colMeta, len(s.Cases))
		for _, c := range s.Cases {
			colMeta = appendSvarint(colMeta, c)
		}
		if s.DynamicLang {
			colMeta = appendSvarint(colMeta, -1)
		} else {
			colMeta = appendSvarint(colMeta, s.FixedLangID)
		}
		colMeta = appendSvarint(colMeta, s.CharWidth)
		colMeta = appendSvarint(colMeta, s.EReplace)
		colMeta = appendSvarint(colMeta, s.DReplace)
		if s.EBuffer == nil {
			colMeta = appendSvarint(colMeta, 0)
		} else {
			colMeta = appendSvarint(colMeta, 1)
		}

		// dBuffer delta
		colSlices = appendSvarint(colSlices, len(s.DBuffer))
		prev := 0
		for _, v := range s.DBuffer {
			colSlices = appendSvarint(colSlices, v-prev)
			prev = v
		}

		// dBuffer2 delta
		colSlices = appendSvarint(colSlices, len(s.DBuffer2))
		prev = 0
		for _, v := range s.DBuffer2 {
			colSlices = appendSvarint(colSlices, v-prev)
			prev = v
		}

		if s.EBuffer != nil {
			derived := deriveBaseEBuffer(s.DBuffer, s.DBuffer2)
			var overrides []kvPair
			var removals []int
			for k, v := range s.EBuffer {
				if dv, ok := derived[k]; !ok || dv != v {
					overrides = append(overrides, kvPair{k: k, v: v})
				}
			}
			for k := range derived {
				if _, ok := s.EBuffer[k]; !ok {
					removals = append(removals, k)
				}
			}
			sort.Slice(overrides, func(i, j int) bool { return overrides[i].k < overrides[j].k })
			sort.Ints(removals)

			colKeys = appendSvarint(colKeys, len(overrides))
			pk, pv := 0, 0
			for _, p := range overrides {
				colKeys = appendSvarint(colKeys, p.k-pk)
				colVals = appendSvarint(colVals, p.v-pv)
				pk, pv = p.k, p.v
			}

			colKeys = appendSvarint(colKeys, len(removals))
			pk = 0
			for _, k := range removals {
				colKeys = appendSvarint(colKeys, k-pk)
				pk = k
			}
		}

		// leading map
		var lead []kvPair
		for k, v := range s.Leading {
			lead = append(lead, kvPair{k: k, v: v})
		}
		sort.Slice(lead, func(i, j int) bool { return lead[i].k < lead[j].k })
		colKeys = appendSvarint(colKeys, len(lead))
		pk, pv := 0, 0
		for _, p := range lead {
			colKeys = appendSvarint(colKeys, p.k-pk)
			colVals = appendSvarint(colVals, p.v-pv)
			pk, pv = p.k, p.v
		}
	}

	var raw bytes.Buffer
	var hdr [20]byte
	binary.LittleEndian.PutUint32(hdr[0:4], mustUint32(len(specs)))
	binary.LittleEndian.PutUint32(hdr[4:8], mustUint32(len(colMeta)))
	binary.LittleEndian.PutUint32(hdr[8:12], mustUint32(len(colSlices)))
	binary.LittleEndian.PutUint32(hdr[12:16], mustUint32(len(colKeys)))
	binary.LittleEndian.PutUint32(hdr[16:20], mustUint32(len(colVals)))
	raw.Write(hdr[:])
	raw.Write(colMeta)
	raw.Write(colSlices)
	raw.Write(colKeys)
	raw.Write(colVals)

	var gzBuf bytes.Buffer
	gw, err := gzip.NewWriterLevel(&gzBuf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := gw.Write(raw.Bytes()); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return gzBuf.Bytes(), nil
}

func mustUint32(n int) uint32 {
	if n < 0 || int64(n) > math.MaxUint32 {
		panic("integer out of uint32 range")
	}
	return uint32(n)
}

func readSvarint(b []byte, off *int) int {
	v, n := binary.Varint(b[*off:])
	if n <= 0 {
		panic("corrupt go-ora packed charset stream")
	}
	*off += n
	return int(v)
}

func unpackConverterSpec(gzData []byte, targetLangID int) *converterSpec {
	gr, err := gzip.NewReader(bytes.NewReader(gzData))
	if err != nil {
		return nil
	}
	raw, err := io.ReadAll(gr)
	_ = gr.Close()
	if err != nil || len(raw) < 20 {
		return nil
	}

	count := int(binary.LittleEndian.Uint32(raw[0:4]))
	lenMeta := int(binary.LittleEndian.Uint32(raw[4:8]))
	lenSlices := int(binary.LittleEndian.Uint32(raw[8:12]))
	lenKeys := int(binary.LittleEndian.Uint32(raw[12:16]))
	lenVals := int(binary.LittleEndian.Uint32(raw[16:20]))
	if 20+lenMeta+lenSlices+lenKeys+lenVals > len(raw) {
		return nil
	}

	colMeta := raw[20 : 20+lenMeta]
	colSlices := raw[20+lenMeta : 20+lenMeta+lenSlices]
	colKeys := raw[20+lenMeta+lenSlices : 20+lenMeta+lenSlices+lenKeys]
	colVals := raw[20+lenMeta+lenSlices+lenKeys : 20+lenMeta+lenSlices+lenKeys+lenVals]

	offMeta, offSlices, offKeys, offVals := 0, 0, 0, 0

	for i := 0; i < count; i++ {
		numCases := readSvarint(colMeta, &offMeta)
		matched := false
		for cIdx := 0; cIdx < numCases; cIdx++ {
			cid := readSvarint(colMeta, &offMeta)
			if cid == targetLangID {
				matched = true
			}
		}
		fixedLang := readSvarint(colMeta, &offMeta)
		charWidth := readSvarint(colMeta, &offMeta)
		eReplace := readSvarint(colMeta, &offMeta)
		dReplace := readSvarint(colMeta, &offMeta)
		hasEBuf := readSvarint(colMeta, &offMeta) == 1

		dLen := readSvarint(colSlices, &offSlices)
		var dBuf []int
		if matched && dLen > 0 {
			dBuf = make([]int, dLen)
		}
		prev := 0
		for j := 0; j < dLen; j++ {
			prev += readSvarint(colSlices, &offSlices)
			if matched {
				dBuf[j] = prev
			}
		}

		d2Len := readSvarint(colSlices, &offSlices)
		var dBuf2 []int
		if matched && d2Len > 0 {
			dBuf2 = make([]int, d2Len)
		}
		prev = 0
		for j := 0; j < d2Len; j++ {
			prev += readSvarint(colSlices, &offSlices)
			if matched {
				dBuf2[j] = prev
			}
		}

		var eBuf map[int]int
		if hasEBuf {
			ovLen := readSvarint(colKeys, &offKeys)
			if matched {
				eBuf = deriveBaseEBuffer(dBuf, dBuf2)
				if eBuf == nil {
					eBuf = make(map[int]int, ovLen)
				}
			}
			pk, pv := 0, 0
			for j := 0; j < ovLen; j++ {
				pk += readSvarint(colKeys, &offKeys)
				pv += readSvarint(colVals, &offVals)
				if matched {
					eBuf[pk] = pv
				}
			}

			remLen := readSvarint(colKeys, &offKeys)
			pk = 0
			for j := 0; j < remLen; j++ {
				pk += readSvarint(colKeys, &offKeys)
				if matched {
					delete(eBuf, pk)
				}
			}
		}

		leadLen := readSvarint(colKeys, &offKeys)
		var leadMap map[int]int
		if matched && leadLen > 0 {
			leadMap = make(map[int]int, leadLen)
		}
		pk, pv := 0, 0
		for j := 0; j < leadLen; j++ {
			pk += readSvarint(colKeys, &offKeys)
			pv += readSvarint(colVals, &offVals)
			if matched {
				leadMap[pk] = pv
			}
		}

		if matched {
			resolvedLang := fixedLang
			if fixedLang < 0 {
				resolvedLang = targetLangID
			}
			return &converterSpec{
				FixedLangID: resolvedLang,
				CharWidth:   charWidth,
				EReplace:    eReplace,
				DReplace:    dReplace,
				DBuffer:     dBuf,
				DBuffer2:    dBuf2,
				EBuffer:     eBuf,
				Leading:     leadMap,
			}
		}
	}
	return nil
}
