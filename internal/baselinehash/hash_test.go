package baselinehash

import "testing"

func TestSHA256MatchesPinnedPresentationContract(t *testing.T) {
	tests := []struct {
		name                       string
		entity, field, kind, value string
		values                     []string
		want                       string
	}{
		{"empty scalar", "decision:id", "title", "scalar", "", nil, "4b824643062f3cde4ed01998c9b420d1fe4b4d191d62bb44be7e3eb3f27ed9d1"},
		{"escaped scalar", "decision:id", "title", "scalar", "line\n中文", nil, "6bf2d1302a88ecff8e50e8a5e8f4ef3f03f40f5625f4e5a511d149fa51b5efe0"},
		{"nil list", "decision:id", "tags", "list", "", nil, "6f039a1de7e3de0cfad12f19134623d696b3a14e750f0e0822fad063471cd489"},
		{"empty list", "decision:id", "tags", "list", "", []string{}, "5a4520b41be588a3dd9e8ccfa69730daa22360907e29b42b6c691cc5cb6cc5d9"},
		{"ordered list", "decision:id", "tags", "list", "", []string{"二", "one"}, "66e34e9ff1e021802214cd47572dd43795379f7097b687621a51fa6185772dec"},
		{"reordered list", "decision:id", "tags", "list", "", []string{"one", "二"}, "c1d66c2f2608e936b0254783855da3dbbadc581b167a3cd5078e3108b05ec359"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SHA256(tc.entity, tc.field, tc.kind, tc.value, tc.values); got != tc.want {
				t.Fatalf("SHA256() = %s, want %s", got, tc.want)
			}
		})
	}
}
