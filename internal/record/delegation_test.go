package record

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func sampleRequest() *Request {
	base := sampleRecord()

	return &Request{
		Envelope: Envelope{
			Version:       ParentVersion,
			ID:            "01JCCCCCCCCCCCCCCCCCCCCCCC",
			Footprint:     NoneFootprint(),
			ClearWhenDead: true,
			Liveness:      LivenessProcess,
			Machine:       base.Machine,
			Holder:        base.Holder,
		},
		Parent:         testID,
		Domain:         base.Domain,
		ActingFor:      "ripta",
		PermissionMode: "plan",
		Description:    "the api",
		RequestedAt:    time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
	}
}

func TestCurrentMinorCoversTheSubLeaseFields(t *testing.T) {
	if ParentVersion.Newer() {
		t.Errorf("%s should be a version this binary writes", ParentVersion)
	}

	if PathVersion.Minor >= ParentVersion.Minor {
		t.Errorf("a plain path record is stamped %s, which must stay below %s", PathVersion, ParentVersion)
	}

	if !(Version{Major: CurrentMajor, Minor: CurrentMinor + 1}).Newer() {
		t.Error("the minor after the current one should read as newer")
	}
}

func TestParentRoundTripsAndPlainRecordsOmitIt(t *testing.T) {
	plain := mustEncode(t, sampleRecord())
	if bytes.Contains(plain, []byte(`"parent"`)) {
		t.Errorf("a plain lease carries a parent field: %s", plain)
	}

	child := sampleRecord()
	child.Version, child.Parent = ParentVersion, testID

	got, err := Decode(mustEncode(t, child))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if got.Parent != testID || got.Version != ParentVersion {
		t.Errorf("parent = %q, version = %s", got.Parent, got.Version)
	}
}

func TestRequestRoundTripsWithAFootprintOfNone(t *testing.T) {
	want := sampleRequest()

	data, err := want.Encode()
	if err != nil {
		t.Fatal(err)
	}

	got, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}

	again, err := got.Encode()
	if err != nil || !bytes.Equal(again, data) {
		t.Errorf("round trip changed the request:\n got %s\nwant %s (%v)", again, data, err)
	}

	if got.Footprint.Kind != FootprintNone || got.Parent != testID || got.State != "" {
		t.Errorf("request = %+v", got)
	}
}

func TestRequestWithoutAParentOrAPathDomainIsUnusable(t *testing.T) {
	noParent := sampleRequest()
	noParent.Parent = ""

	data, err := noParent.Encode()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := DecodeRequest(data); !errors.Is(err, ErrUnusable) {
		t.Errorf("DecodeRequest of a request with no parent = %v, want unusable", err)
	}
}

func TestRequestOfAHigherMinorIsNeverRewritten(t *testing.T) {
	req := sampleRequest()
	req.Version = Version{Major: CurrentMajor, Minor: CurrentMinor + 1}

	if _, err := req.Encode(); !errors.Is(err, ErrNewerRecord) {
		t.Errorf("Encode = %v, want ErrNewerRecord", err)
	}
}

func TestNoticeIDsAreDerivedNamesAndStillDecode(t *testing.T) {
	base := sampleRecord()

	n := &Notice{
		Envelope: Envelope{
			Version:       ParentVersion,
			ID:            NoticeID("01JCCCCCCCCCCCCCCCCCCCCCCC", CauseRequestGranted),
			Footprint:     NoneFootprint(),
			ClearWhenDead: true,
			Liveness:      LivenessProcess,
			Machine:       base.Machine,
			Holder:        base.Holder,
		},
		Cause:  CauseRequestGranted,
		Target: "01JCCCCCCCCCCCCCCCCCCCCCCC",
		Detail: "your request was granted",
		Paths:  []string{"/repo/src"},
		At:     time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
	}

	data, err := n.Encode()
	if err != nil {
		t.Fatal(err)
	}

	got, err := DecodeNotice(data)
	if err != nil {
		t.Fatalf("DecodeNotice rejected a derived ID: %v", err)
	}

	if got.ID != "01JCCCCCCCCCCCCCCCCCCCCCCC-request-granted" || got.Cause != CauseRequestGranted {
		t.Errorf("notice = %+v", got)
	}
}
