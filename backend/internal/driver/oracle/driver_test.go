package oracle

import "testing"

func TestParseOracleSessionTarget(t *testing.T) {
	sid, ser, inst, hasInst, err := parseOracleSessionTarget("142,58901")
	if err != nil || sid != 142 || ser != 58901 || hasInst || inst != 0 {
		t.Fatalf("unexpected parse result: sid=%d ser=%d inst=%d hasInst=%v err=%v", sid, ser, inst, hasInst, err)
	}
	sid, ser, inst, hasInst, err = parseOracleSessionTarget(" 142,58901,@2 ")
	if err != nil || sid != 142 || ser != 58901 || !hasInst || inst != 2 {
		t.Fatalf("unexpected RAC parse result: sid=%d ser=%d inst=%d hasInst=%v err=%v", sid, ser, inst, hasInst, err)
	}
	for _, bad := range []string{"", "142", "142,abc", "142,58901;DROP", "142,58901,2", "142,58901,@0", "142,58901,@-1"} {
		if _, _, _, _, err := parseOracleSessionTarget(bad); err == nil {
			t.Fatalf("expected error for invalid Oracle session ID %q", bad)
		}
	}
}
