package store

import "testing"

const testUUID = "0f8fad5b-d9cb-469f-a165-70867728950e"

func TestIncomingKey(t *testing.T) {
	k, err := IncomingKey(testUUID)
	if err != nil || k != "incoming/"+testUUID {
		t.Fatalf("IncomingKey = %q, %v", k, err)
	}
	if _, err := IncomingKey("../x"); err == nil {
		t.Fatal("non-UUID accepted")
	}
}

func TestObjectKey(t *testing.T) {
	k, err := ObjectKey("photos", testUUID, "thumb")
	if err != nil || k != "photos/"+testUUID+"/thumb" {
		t.Fatalf("ObjectKey = %q, %v", k, err)
	}
	bad := [][3]string{
		{"photos", "not-a-uuid", "thumb"},
		{"../x", testUUID, "thumb"},
		{"A", testUUID, "thumb"},
		{"photos", testUUID, ""},
	}
	for _, b := range bad {
		if _, err := ObjectKey(b[0], b[1], b[2]); err == nil {
			t.Errorf("ObjectKey%v accepted", b)
		}
	}
}

func TestValidKey(t *testing.T) {
	for _, k := range []string{"", "a/../b", "/a", "a//b", "a/.tmp/b", "a/", ".."} {
		if ValidKey(k) == nil {
			t.Errorf("ValidKey(%q) = nil", k)
		}
	}
	if err := ValidKey("incoming/" + testUUID); err != nil {
		t.Errorf("ValidKey: %v", err)
	}
}
