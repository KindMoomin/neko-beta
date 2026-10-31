package api

import "testing"

func TestPodDeepCopyIsIndependent(t *testing.T) {
	orig := &Pod{
		TypeMeta: TypeMeta{Kind: "Pod", APIVersion: APIVersion},
		Meta: ObjectMeta{
			Name:      "web",
			Namespace: "default",
			Labels:    map[string]string{"app": "web"},
			OwnerRefs: []OwnerRef{{Kind: "ReplicaSet", Name: "web-abc"}},
		},
		Spec: PodSpec{
			Containers: []Container{{Name: "c", Image: "nginx", Ports: []int{80}}},
		},
	}

	cp := orig.DeepCopyObject().(*Pod)

	// Mutate every nested reference type on the copy.
	cp.Meta.Labels["app"] = "changed"
	cp.Meta.Labels["new"] = "x"
	cp.Meta.OwnerRefs[0].Name = "changed"
	cp.Spec.Containers[0].Image = "changed"
	cp.Spec.Containers[0].Ports[0] = 443
	cp.Spec.NodeName = "node9"

	// Original must be untouched.
	if got := orig.Meta.Labels["app"]; got != "web" {
		t.Errorf("labels aliased: orig app = %q, want web", got)
	}
	if _, ok := orig.Meta.Labels["new"]; ok {
		t.Error("labels map aliased: new key leaked into original")
	}
	if got := orig.Meta.OwnerRefs[0].Name; got != "web-abc" {
		t.Errorf("ownerRefs aliased: orig = %q, want web-abc", got)
	}
	if got := orig.Spec.Containers[0].Image; got != "nginx" {
		t.Errorf("containers aliased: orig image = %q, want nginx", got)
	}
	if got := orig.Spec.Containers[0].Ports[0]; got != 80 {
		t.Errorf("ports aliased: orig = %d, want 80", got)
	}
	if orig.Spec.NodeName != "" {
		t.Errorf("nodeName aliased: orig = %q, want empty", orig.Spec.NodeName)
	}
}

func TestDeepCopyNilMapsStayNil(t *testing.T) {
	orig := &Namespace{Meta: ObjectMeta{Name: "x"}}
	cp := orig.DeepCopyObject().(*Namespace)
	if cp.Meta.Labels != nil {
		t.Error("nil labels should stay nil after copy")
	}
}
