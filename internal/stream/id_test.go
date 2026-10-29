package stream

import "testing"

func TestParseID(t *testing.T) {
	valid := []struct {
		in        string
		id        uint64
		serviceID int
	}{
		{"3273601024", 3273601024, 1024},
		{"1", 1, 1},
		{"007", 7, 7}, // leading zeros are fine; the canonical id is used upstream
		{"65535", 65535, 65535},
		{"165535", 165535, 65535},
		{"100001", 100001, 1},
	}
	for _, tc := range valid {
		id, sid, err := ParseID(tc.in)
		if err != nil || id != tc.id || sid != tc.serviceID {
			t.Errorf("ParseID(%q) = (%d, %d, %v), want (%d, %d, nil)", tc.in, id, sid, err, tc.id, tc.serviceID)
		}
	}

	invalid := []string{
		"",
		"0",
		"100000", // serviceId 0 would turn tsreadex's service filter off (-n 0)
		"200000",
		"65536",
		"99999",
		"9999999999",
		"+1",
		"-1",
		"1a",
		" 1",
		"1 ",
		"1.5",
		"0x10",
		"12345678901", // 11 digits
		"٣",           // non-ASCII digit
	}
	for _, in := range invalid {
		if _, _, err := ParseID(in); err == nil {
			t.Errorf("ParseID(%q) = ok, want an error", in)
		}
	}
}
