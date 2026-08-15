package configutil

import (
	"crypto/sha256"
	"strings"
	"testing"
)

func TestSplitCSV(t *testing.T) {
	if got := strings.Join(SplitCSV(" managerd, observer ,,managerd "), ","); got != "managerd,observer,managerd" {
		t.Fatalf("SplitCSV() = %q", got)
	}
}

func TestParseSHA256Map(t *testing.T) {
	digest := sha256.Sum256([]byte("synthetic-secret"))
	encoded := fmtDigest(digest)
	parsed, err := ParseSHA256Map(" managerd = " + encoded)
	if err != nil {
		t.Fatalf("ParseSHA256Map(): %v", err)
	}
	if parsed["managerd"] != digest {
		t.Fatal("parsed digest mismatch")
	}
}

func TestParseSHA256MapRefusesMalformedInput(t *testing.T) {
	digest := sha256.Sum256([]byte("synthetic-secret"))
	encoded := fmtDigest(digest)
	tests := []string{
		"managerd",
		"managerd=short",
		"managerd=" + strings.Repeat("z", 64),
		"managerd=" + encoded + ",managerd=" + encoded,
		"managerd=" + encoded + ",",
	}
	for _, input := range tests {
		if _, err := ParseSHA256Map(input); err == nil {
			t.Fatalf("ParseSHA256Map(%q) succeeded", input)
		}
	}
}

func fmtDigest(digest [sha256.Size]byte) string {
	const digits = "0123456789abcdef"
	encoded := make([]byte, sha256.Size*2)
	for index, value := range digest {
		encoded[index*2] = digits[value>>4]
		encoded[index*2+1] = digits[value&0x0f]
	}
	return string(encoded)
}
