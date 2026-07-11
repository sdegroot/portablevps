package cli

import "testing"

// TestAgeKeyFieldMatchesKeystoreContract locks the escrowed field's label to
// "age-key" — the keystore reads it as op://<vault>/<server>/age-key, so a
// rename here would silently break key resolution after escrow.
func TestAgeKeyFieldMatchesKeystoreContract(t *testing.T) {
	f := ageKeyField("AGE-SECRET-KEY-EXAMPLE")
	if f["label"] != "age-key" {
		t.Errorf("field label = %v, want age-key (keystore reads /age-key)", f["label"])
	}
	if f["type"] != "CONCEALED" {
		t.Errorf("field type = %v, want CONCEALED", f["type"])
	}
	if f["value"] != "AGE-SECRET-KEY-EXAMPLE" {
		t.Errorf("field value not carried through: %v", f["value"])
	}
}
