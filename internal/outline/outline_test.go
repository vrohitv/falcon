package outline

import "testing"

func TestOutlineGoAndOthers(t *testing.T) {
	g := Of("a.go", "package a\n\ntype T struct{}\n\nfunc (t *T) M() {}\n\nfunc F() {}\n\nconst C = 1\n")
	want := []string{"T", "T.M", "F", "C"}
	if len(g) != len(want) {
		t.Fatalf("%+v", g)
	}
	for i, d := range g {
		if d.Name != want[i] {
			t.Fatalf("%d: %+v", i, d)
		}
	}
	for path, src := range map[string]string{
		"a.py":   "class A:\n    def m(self):\n        pass\n",
		"a.ts":   "export function f() {}\nexport class B {}\n",
		"a.rs":   "pub fn greet() {}\nstruct S;\n",
		"a.c":    "int main(void) {\n  if (x) {}\n}\n",
		"A.java": "public class A {\n  public static void main(String[] a) {}\n}\n",
	} {
		if len(Of(path, src)) < 1 {
			t.Errorf("%s: nothing found", path)
		}
	}
	if d := Of("a.c", "int main(void) {\n  if (x) {}\n}\n"); len(d) != 1 || d[0].Name != "main" {
		t.Fatalf("%+v", d)
	}
}
