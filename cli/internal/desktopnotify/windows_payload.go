package desktopnotify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
)

func windowsToast(message Message) (string, string, error) {
	content := struct {
		XMLName    xml.Name `xml:"toast"`
		Activation string   `xml:"activationType,attr"`
		Launch     string   `xml:"launch,attr"`
		Visual     struct {
			Binding struct {
				Template string   `xml:"template,attr"`
				Text     []string `xml:"text"`
			} `xml:"binding"`
		} `xml:"visual"`
	}{Activation: "protocol", Launch: message.URL}
	content.Visual.Binding.Template = "ToastGeneric"
	content.Visual.Binding.Text = []string{message.Title, message.Body}
	data, err := xml.Marshal(content)
	if err == nil && len(data) > 5*1024 {
		return "", "", errors.New("Windows notification XML exceeds 5 KB")
	}
	digest := sha256.Sum256([]byte(message.ID))
	return string(data), hex.EncodeToString(digest[:8]), err
}
