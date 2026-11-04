package eporner

import "testing"

// Эталон получен функцией calc_hash из yt-dlp.
func TestCalcHash(t *testing.T) {
	if got := CalcHash("0123456789abcdef0123456789abcdef"); got != "bd4yv1275p7zbd4yv1275p7z" {
		t.Errorf("CalcHash = %q", got)
	}
}
