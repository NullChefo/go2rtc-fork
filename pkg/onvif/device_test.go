package onvif

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseProfiles(t *testing.T) {
	tests := []struct {
		name   string
		xml    string
		tokens []string
	}{
		{
			name: "Dahua compact",
			xml: `<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema"><s:Body><trt:GetProfilesResponse><trt:Profiles token="MediaProfile00000" fixed="true"><tt:Name>MainStream</tt:Name></trt:Profiles><trt:Profiles token="MediaProfile00001" fixed="true"><tt:Name>SubStream</tt:Name></trt:Profiles></trt:GetProfilesResponse></s:Body></s:Envelope>`,
			tokens: []string{"MediaProfile00000", "MediaProfile00001"},
		},
		{
			name: "Hikvision formatted with extra attrs",
			xml: `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
    <s:Body>
        <trt:GetProfilesResponse>
            <trt:Profiles fixed="true" token="Profile_1">
                <tt:Name>mainStream</tt:Name>
            </trt:Profiles>
            <trt:Profiles fixed="true" token="Profile_2">
                <tt:Name>subStream</tt:Name>
            </trt:Profiles>
        </trt:GetProfilesResponse>
    </s:Body>
</s:Envelope>`,
			tokens: []string{"Profile_1", "Profile_2"},
		},
		{
			name:   "single profile",
			xml:    `<trt:GetProfilesResponse><trt:Profiles token="0"><tt:Name>stream</tt:Name></trt:Profiles></trt:GetProfilesResponse>`,
			tokens: []string{"0"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profiles := parseProfiles([]byte(test.xml))
			got := make([]string, len(profiles))
			for i, p := range profiles {
				got[i] = p.Token
			}
			require.Equal(t, test.tokens, got)
		})
	}
}
