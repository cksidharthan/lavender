package pages

// Theme is a colour scheme the user can pick. Its colours are in static/themes.css, under [data-theme="<ID>"].
type Theme struct {
	ID          string
	Name        string
	Description string
}

// DefaultTheme is the theme of a device that picked none.
const DefaultTheme = "system"

// Themes are the themes in the order they are shown. System is light or dark like the device.
var Themes = []Theme{
	{ID: "system", Name: "System", Description: "Light or dark, like your device"},
	{ID: "light", Name: "Light", Description: "The default look"},
	{ID: "dark", Name: "Dark", Description: "Easy on the eyes at night"},
	{ID: "tokyonight", Name: "Tokyo Night", Description: "Deep blue with a soft glow"},
	{ID: "catppuccin-mocha", Name: "Catppuccin Mocha", Description: "Warm, dark and pastel"},
	{ID: "catppuccin-latte", Name: "Catppuccin Latte", Description: "Warm, light and pastel"},
	{ID: "nord", Name: "Nord", Description: "Cool arctic blues"},
	{ID: "dracula", Name: "Dracula", Description: "Dark with vivid purples"},
	{ID: "gruvbox", Name: "Gruvbox", Description: "Retro earthy tones"},
}

// ThemeID is the theme to show as picked: the one of the cookie when it is a known theme, else the default.
func ThemeID(saved string) string {
	for _, t := range Themes {
		if t.ID == saved {
			return saved
		}
	}

	return DefaultTheme
}
