package product

import "testing"

func TestExtractCloudinaryPublicID(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		// Standard upload with version
		{
			"https://res.cloudinary.com/ns69k7e5/image/upload/v1234567890/rok-store-preset-unsigned/myimage.jpg",
			"rok-store-preset-unsigned/myimage",
		},
		// No version segment
		{
			"https://res.cloudinary.com/ns69k7e5/image/upload/rok-store-preset-unsigned/myimage.jpg",
			"rok-store-preset-unsigned/myimage",
		},
		// PNG extension
		{
			"https://res.cloudinary.com/ns69k7e5/image/upload/v1700000000/myimage.png",
			"myimage",
		},
		// WEBP extension
		{
			"https://res.cloudinary.com/ns69k7e5/image/upload/v1700000000/myimage.webp",
			"myimage",
		},
		// No folder, no version
		{
			"https://res.cloudinary.com/ns69k7e5/image/upload/myimage.jpg",
			"myimage",
		},
		// Not a Cloudinary URL — should return empty
		{
			"https://example.com/image.jpg",
			"",
		},
	}

	for _, tt := range tests {
		got := extractCloudinaryPublicID(tt.url)
		if got != tt.expected {
			t.Errorf("extractCloudinaryPublicID(%q)\n  got:  %q\n  want: %q", tt.url, got, tt.expected)
		}
	}
}
