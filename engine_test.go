package scriptagent

import "testing"

func TestLimitOutput(t *testing.T) {
	out := []Output{
		{Kind: "text", Stream: "stdout", Text: "hello"},
		{Kind: "text", Stream: "stderr", Text: "world"},
	}
	got, limited := limitOutput(out, 100)
	if limited {
		t.Fatal("limit hit unexpectedly")
	}
	if len(got) != 2 {
		t.Fatalf("output = %+v", got)
	}

	got, limited = limitOutput(out, 7)
	if !limited {
		t.Fatal("limit not reported")
	}
	if len(got) != 2 || got[0].Text != "hello" || got[1].Text != "wo" {
		t.Fatalf("output = %+v", got)
	}

	img := []Output{{Kind: "image", Stream: "stdout", MIME: "image/png", Data: []byte{1, 2, 3, 4}}}
	got, limited = limitOutput(img, 2)
	if !limited || len(got) != 1 || len(got[0].Data) != 2 {
		t.Fatalf("image output = %+v limited=%v", got, limited)
	}
}

func TestLimitOutputUTF8Boundary(t *testing.T) {
	// "é" is two bytes. A one-byte limit must not split it.
	out := []Output{{Kind: "text", Stream: "stdout", Text: "é"}}
	got, limited := limitOutput(out, 1)
	if !limited {
		t.Fatal("limit not reported")
	}
	if got[0].Text != "" {
		t.Fatalf("text = %q, want empty at a rune boundary", got[0].Text)
	}
}
