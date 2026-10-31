// Package api is the neko-box object model: the Kubernetes-shaped kinds
// neko-box stores and reconciles. Single version "neko/v1"; no conversion, no
// OpenAPI. These are plain data types with JSON tags (the wire codec) — the
// native authoring format is TOML, decoded into these same structs.
//
// Every stored kind implements Object. Bodies that need real logic are left as
// panic("not implemented") — this package is a compiling skeleton (architect
// Phase D), not an implementation.
package api

// APIVersion is the single, only version neko-box serves.
const APIVersion = "neko/v1"

// TypeMeta identifies an object's kind. Inlined into every object.
type TypeMeta struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
}

// OwnerRef links an object to the object that controls it (e.g. a Pod to its
// ReplicaSet), for cascading delete.
type OwnerRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// ObjectMeta is the common metadata on every object.
type ObjectMeta struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace,omitempty"`
	UID       string            `json:"uid"`
	// Revision is store-assigned and doubles as the optimistic-concurrency token.
	Revision    uint64            `json:"revision"`
	Labels      map[string]string `json:"labels,omitempty"`
	OwnerRefs   []OwnerRef        `json:"ownerRefs,omitempty"`
	CreationRev uint64            `json:"creationRev,omitempty"`
	// Deleting marks a tombstone during cascading delete.
	Deleting bool `json:"deleting,omitempty"`
}

// Object is the contract the store and transport layers depend on. Deliberately
// tiny: identity, type, and a deep copy so the store never leaks aliased pointers.
type Object interface {
	GetMeta() *ObjectMeta
	GetType() TypeMeta
	DeepCopyObject() Object
}

// --- Pod -------------------------------------------------------------------

type Pod struct {
	TypeMeta `json:",inline"`
	Meta     ObjectMeta `json:"metadata"`
	Spec     PodSpec    `json:"spec"`
	Status   PodStatus  `json:"status"`
}

type PodSpec struct {
	// NodeName is set by placement; the kubelet acts only on pods bound to it.
	NodeName   string      `json:"nodeName,omitempty"`
	Containers []Container `json:"containers"`
}

type Container struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	Ports []int  `json:"ports,omitempty"`
}

type PodStatus struct {
	// Phase: Pending | Scheduled | Running | Succeeded | Failed
	Phase string `json:"phase"`
	PodIP string `json:"podIP,omitempty"`
	Ready bool   `json:"ready"`
}

func (p *Pod) GetMeta() *ObjectMeta { return &p.Meta }
func (p *Pod) GetType() TypeMeta    { return p.TypeMeta }
func (p *Pod) DeepCopyObject() Object {
	cp := *p
	cp.Meta = copyMeta(p.Meta)
	cp.Spec = copyPodSpec(p.Spec)
	return &cp
}

// --- Namespace -------------------------------------------------------------

type Namespace struct {
	TypeMeta `json:",inline"`
	Meta     ObjectMeta `json:"metadata"`
}

func (n *Namespace) GetMeta() *ObjectMeta { return &n.Meta }
func (n *Namespace) GetType() TypeMeta    { return n.TypeMeta }
func (n *Namespace) DeepCopyObject() Object {
	cp := *n
	cp.Meta = copyMeta(n.Meta)
	return &cp
}

// --- Deployment / ReplicaSet (M2) -----------------------------------------

type Deployment struct {
	TypeMeta `json:",inline"`
	Meta     ObjectMeta     `json:"metadata"`
	Spec     DeploymentSpec `json:"spec"`
	Status   WorkloadStatus `json:"status"`
}

type DeploymentSpec struct {
	Replicas int               `json:"replicas"`
	Selector map[string]string `json:"selector"`
	Template PodSpec           `json:"template"`
}

func (d *Deployment) GetMeta() *ObjectMeta { return &d.Meta }
func (d *Deployment) GetType() TypeMeta    { return d.TypeMeta }
func (d *Deployment) DeepCopyObject() Object {
	cp := *d
	cp.Meta = copyMeta(d.Meta)
	cp.Spec.Selector = copyStringMap(d.Spec.Selector)
	cp.Spec.Template = copyPodSpec(d.Spec.Template)
	return &cp
}

type ReplicaSet struct {
	TypeMeta `json:",inline"`
	Meta     ObjectMeta     `json:"metadata"`
	Spec     ReplicaSetSpec `json:"spec"`
	Status   WorkloadStatus `json:"status"`
}

type ReplicaSetSpec struct {
	Replicas int               `json:"replicas"`
	Selector map[string]string `json:"selector"`
	Template PodSpec           `json:"template"`
}

type WorkloadStatus struct {
	Replicas      int `json:"replicas"`
	ReadyReplicas int `json:"readyReplicas"`
}

func (r *ReplicaSet) GetMeta() *ObjectMeta { return &r.Meta }
func (r *ReplicaSet) GetType() TypeMeta    { return r.TypeMeta }
func (r *ReplicaSet) DeepCopyObject() Object {
	cp := *r
	cp.Meta = copyMeta(r.Meta)
	cp.Spec.Selector = copyStringMap(r.Spec.Selector)
	cp.Spec.Template = copyPodSpec(r.Spec.Template)
	return &cp
}

// --- Service / EndpointSlice (M2) -----------------------------------------

type Service struct {
	TypeMeta `json:",inline"`
	Meta     ObjectMeta  `json:"metadata"`
	Spec     ServiceSpec `json:"spec"`
}

type ServiceSpec struct {
	ClusterIP string            `json:"clusterIP,omitempty"`
	Selector  map[string]string `json:"selector"`
	Ports     []ServicePort     `json:"ports"`
}

type ServicePort struct {
	Port       int    `json:"port"`
	TargetPort int    `json:"targetPort"`
	Protocol   string `json:"protocol,omitempty"`
}

func (s *Service) GetMeta() *ObjectMeta { return &s.Meta }
func (s *Service) GetType() TypeMeta    { return s.TypeMeta }
func (s *Service) DeepCopyObject() Object {
	cp := *s
	cp.Meta = copyMeta(s.Meta)
	cp.Spec.Selector = copyStringMap(s.Spec.Selector)
	if s.Spec.Ports != nil {
		ports := make([]ServicePort, len(s.Spec.Ports))
		copy(ports, s.Spec.Ports)
		cp.Spec.Ports = ports
	}
	return &cp
}

type EndpointSlice struct {
	TypeMeta  `json:",inline"`
	Meta      ObjectMeta `json:"metadata"`
	Endpoints []Endpoint `json:"endpoints"`
}

type Endpoint struct {
	PodUID string `json:"podUID"`
	IP     string `json:"ip"`
	Port   int    `json:"port"`
	Ready  bool   `json:"ready"`
}

func (e *EndpointSlice) GetMeta() *ObjectMeta { return &e.Meta }
func (e *EndpointSlice) GetType() TypeMeta    { return e.TypeMeta }
func (e *EndpointSlice) DeepCopyObject() Object {
	cp := *e
	cp.Meta = copyMeta(e.Meta)
	if e.Endpoints != nil {
		eps := make([]Endpoint, len(e.Endpoints))
		copy(eps, e.Endpoints)
		cp.Endpoints = eps
	}
	return &cp
}
