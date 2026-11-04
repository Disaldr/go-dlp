package xhamster

import "testing"

// Векторы сгенерированы эталонной реализацией yt-dlp (_ByteGenerator) для
// открытого текста ниже, по одному на каждый алгоритм.
func TestDecipher(t *testing.T) {
	const plain = "https://video-a.example.com/key=abc/720p.h264.mp4"
	vectors := []struct {
		algo int
		hex  string
	}{
		{1, "012e75b3f8dde4db32cafe7cb98b51534feec13ad02098de7f39780648eee72ad57a5912f3b452accdee56434633b065fc95a216ee51"},
		{2, "0271b7c2f8ef4145c1ef25461e2eeb3e86513b311afb2d92b19f765c13819f013943310d46e6750f97c4d7f3aa81805d075210ac2bb9"},
		{3, "03b4f9d1f8520176c887789ef6f981cb4c5a2b544135880020db46f0bbcada892129e7d73820af2e8999643ebd5f0925048ed927afe7"},
		{4, "04f73be1f8259785a43ae41d683e99265f89691d97ee0da6c87369df27c80302d1eda571ed74183ac40b8d21b3c3f74f8d0e81a93cd5"},
		{5, "053a7ef0f858ff442b4ba0bfbdad38eaca9c098ceed0868e153e80a860e85d58fdd7847265d17869d36ace024535d9ecbfee4cc389aa"},
		{6, "067dc0fff87374f637f5937f617772630d74e4a829cdd266b918ae192f78e06c007d1e2ff960efe20d260e3715ea4c1b500d29ba4a3a"},
		{7, "07c0020ff9672450bc6351ab2522cce5f2462a7b66afc5747603820095b9bad381964873632ae497be9225b43ec2f2179d7541d3cf18"},
	}
	for _, v := range vectors {
		got, err := Decipher(v.hex)
		if err != nil || got != plain {
			t.Errorf("algo %d: got %q, %v", v.algo, got, err)
		}
	}
	if _, err := Decipher("09" + vectors[0].hex[2:]); err == nil {
		t.Error("unknown algorithm must fail")
	}
}

func TestDecipherURL(t *testing.T) {
	hexPart := "012e75b3f8dde4db32cafe7cb98b51534feec13ad02098de7f39780648eee72ad57a5912f3b452accdee56434633b065fc95a216ee51"
	got, err := decipherURL("https://cdn.example.com/" + hexPart + "/master.m3u8")
	want := "https://cdn.example.com/https://video-a.example.com/key=abc/720p.h264.mp4/master.m3u8"
	if err != nil || got != want {
		t.Errorf("decipherURL = %q, %v", got, err)
	}
	if u, err := decipherURL("abcdef"); u != "" || err != nil {
		t.Errorf("short dummy hex must be skipped silently: %q %v", u, err)
	}
	if u, _ := decipherURL("https://plain.example.com/v/720p.mp4"); u != "https://plain.example.com/v/720p.mp4" {
		t.Errorf("plain URL changed: %q", u)
	}
}
