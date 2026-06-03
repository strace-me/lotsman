package subscription

import "testing"

func TestCountryFromName(t *testing.T) {
	cases := map[string]string{
		"🇳🇱 Амстердам, Нидерланды, Extra": "nl",
		"🇰🇿 Алматы, Казахстан":            "kz",
		"🇷🇺 Москва":                       "ru",
		"Умная локация":                   "", // smart location, no flag
		"plain name no flag":              "",
	}
	for name, want := range cases {
		if got := countryFromName(name); got != want {
			t.Errorf("countryFromName(%q) = %q, want %q", name, got, want)
		}
	}
}
