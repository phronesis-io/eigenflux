package integrationidentity

import "testing"

func TestCredentialsAreDistinctAndRejectOtherCredentialKinds(t *testing.T) {
	first, hash, err := NewCredential()
	if err != nil {
		t.Fatal(err)
	}
	second, other, err := NewCredential()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || hash == other || hash == first {
		t.Fatal("credentials must be independent and stored as verification values")
	}
	if actual, err := CredentialHash(first); err != nil || actual != hash {
		t.Fatal("issued credential cannot be verified")
	}
	for _, value := range []string{"", "efv2a_personal", "Bearer " + first, first + "x", "efi_" + first[4:46] + "!"} {
		if _, err := CredentialHash(value); err == nil {
			t.Fatal("malformed or personal credential accepted")
		}
	}
}
