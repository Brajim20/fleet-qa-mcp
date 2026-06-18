package qa

import "testing"

func TestNonTimeKeyIgnoresTimestamp(t *testing.T) {
	a := map[string]interface{}{"t": 1.0, "nav": "red"}
	b := map[string]interface{}{"t": 2.0, "nav": "red"}  // same values, later frame
	c := map[string]interface{}{"t": 3.0, "nav": "blue"} // changed value

	if nonTimeKey(a) != nonTimeKey(b) {
		t.Error("frames with identical values (different t) should collapse")
	}
	if nonTimeKey(a) == nonTimeKey(c) {
		t.Error("frames with different values should not collapse")
	}
}
