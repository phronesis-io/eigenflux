package gatewaysign

import "testing"

func TestCanonicalVector(t *testing.T) {
	got, err := Canonical("POST", "example.org:8443", "/v1/call/7", "z=one+two&a=z&a=%2B", "123", "xyz", "payment", []byte("{}"))
	const want = "EF-GW-V1\nPOST\nexample.org:8443\n/v1/call/7\na=%2B&a=z&z=one%20two\n123\nxyz\npayment\n44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"
	if err != nil || string(got) != want {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := Canonical("GET", "host", "/", "a=%zz", "", "", "", nil); err == nil {
		t.Fatal("accepted invalid query")
	}
}
