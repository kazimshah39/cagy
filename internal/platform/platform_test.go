package platform

import "testing"

func TestValidateSupportsOnlyDarwinArm64(t *testing.T) {
	cases := []struct {
		goos, goarch string
		wantErr      bool
	}{
		{goos: "darwin", goarch: "arm64"},
		{goos: "darwin", goarch: "amd64", wantErr: true},
		{goos: "linux", goarch: "arm64", wantErr: true},
		{goos: "windows", goarch: "amd64", wantErr: true},
		{goos: "", goarch: "", wantErr: true},
	}
	for _, test := range cases {
		err := Validate(test.goos, test.goarch)
		if (err != nil) != test.wantErr {
			t.Fatalf("Validate(%q,%q) err=%v wantErr=%v", test.goos, test.goarch, err, test.wantErr)
		}
		if err != nil && err.Error() != unsupportedMessage {
			t.Fatalf("error=%q", err)
		}
	}
}
