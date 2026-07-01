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

// resolution + codec must be parsed from the VideoEncoderConfiguration, not the
// VideoSourceConfiguration's Bounds attributes.
func TestParseProfileDetails(t *testing.T) {
	xml := `<trt:GetProfilesResponse>
	  <trt:Profiles token="MainStream" fixed="true">
	    <tt:Name>main</tt:Name>
	    <tt:VideoSourceConfiguration token="vsc"><tt:Name>VSC</tt:Name><tt:SourceToken>vs0</tt:SourceToken><tt:Bounds x="0" y="0" width="9999" height="9999"/></tt:VideoSourceConfiguration>
	    <tt:VideoEncoderConfiguration token="vec"><tt:Name>VEC</tt:Name><tt:Encoding>H265</tt:Encoding><tt:Resolution><tt:Width>2560</tt:Width><tt:Height>1440</tt:Height></tt:Resolution></tt:VideoEncoderConfiguration>
	  </trt:Profiles>
	</trt:GetProfilesResponse>`
	p := parseProfiles([]byte(xml))
	require.Len(t, p, 1)
	require.Equal(t, "MainStream", p[0].Token)
	require.Equal(t, "H265", p[0].Codec)
	require.Equal(t, 2560, p[0].Width) // from Resolution, not Bounds width="9999"
	require.Equal(t, 1440, p[0].Height)
	require.Equal(t, "vs0", p[0].VSToken) // SourceToken from the VideoSourceConfiguration
}

// the video codec must come from the VideoEncoderConfiguration even when an
// AudioEncoderConfiguration (with its own Encoding) precedes it.
func TestParseProfileAudioCodecLeak(t *testing.T) {
	xml := `<trt:GetProfilesResponse><trt:Profiles token="P1">
	  <tt:Name>main</tt:Name>
	  <tt:AudioEncoderConfiguration token="aec"><tt:Name>AEC</tt:Name><tt:Encoding>AAC</tt:Encoding></tt:AudioEncoderConfiguration>
	  <tt:VideoEncoderConfiguration token="vec"><tt:Name>VEC</tt:Name><tt:Encoding>H264</tt:Encoding><tt:Resolution><tt:Width>1920</tt:Width><tt:Height>1080</tt:Height></tt:Resolution></tt:VideoEncoderConfiguration>
	</trt:Profiles></trt:GetProfilesResponse>`
	p := parseProfiles([]byte(xml))
	require.Len(t, p, 1)
	require.Equal(t, "H264", p[0].Codec) // not "AAC"
	require.Equal(t, 1920, p[0].Width)
}
