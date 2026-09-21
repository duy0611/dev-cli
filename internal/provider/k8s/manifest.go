package k8s

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/duy0611/dev-cli/internal/dcgen"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// Manifests are hand-rolled structs rather than client-go types. The whole
// provider is a kubectl adapter, so the only thing needed is JSON of the right
// shape; client-go would add a very large dependency to produce the same bytes.

// Annotation keys on the Deployment.
const (
	// annRebuiltAt changes on every rebuild. The image tag never does, so
	// without something in the pod template changing there is no new
	// ReplicaSet, and the cluster keeps running the image it already has.
	annRebuiltAt = "dev.rebuilt-at"
)

// Volume layout inside the pod. Two subPaths on one PVC, not one.
//
// Home has to persist or "postCreate runs once" is a lie: scaling to 0 and back
// gives a fresh container filesystem, so a postCreate that installed something
// into ~/.local or wrote ~/.gitconfig would be silently undone by every stop.
//
// A third subPath carries the agents' configuration when the container asks for
// it. On the same claim rather than a claim of its own: it is deleted with the
// container either way, and a second ReadWriteOnce claim would be a second thing
// to schedule around for no gain.
const (
	volumeName        = "workspace"
	subPathWorkspace  = "workspace"
	subPathHome       = "home"
	subPathState      = "state"
	containerNameMain = "dev"
)

// DevConfig is the part of a merged devcontainer.json this provider needs.
type DevConfig struct {
	WorkspaceFolder string
	RemoteUser      string
	ContainerEnv    map[string]string
}

// HomeDir is where the remote user's home lives inside the container.
func (d DevConfig) HomeDir() string {
	switch d.RemoteUser {
	case "", "root":
		return "/root"
	default:
		return "/home/" + d.RemoteUser
	}
}

// mounts is what the pod mounts off the claim.
//
// Only whether the container persists state is read here, never the volume name
// the generated document carries: that names a docker volume, which means
// nothing to a cluster. The same asymmetry as workspaceMount, which this
// provider also ignores.
func mounts(in manifestInput) []volumeMount {
	out := []volumeMount{
		{Name: volumeName, MountPath: in.Dev.WorkspaceFolder, SubPath: subPathWorkspace},
		{Name: volumeName, MountPath: in.Dev.HomeDir(), SubPath: subPathHome},
	}
	if in.Container.PersistState {
		out = append(out, volumeMount{
			Name: volumeName, MountPath: dcgen.StateDir, SubPath: subPathState,
		})
	}
	return out
}

// manifestInput is everything the objects are built from.
type manifestInput struct {
	Container model.Container
	Config    Config
	Dev       DevConfig
	Image     string
	Env       []provider.EnvVar
	Replicas  int
	RebuiltAt string
}

type objectMeta struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// --- PVC --------------------------------------------------------------------

type pvc struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Metadata   objectMeta `json:"metadata"`
	Spec       pvcSpec    `json:"spec"`
}

type pvcSpec struct {
	AccessModes      []string     `json:"accessModes"`
	Resources        pvcResources `json:"resources"`
	StorageClassName *string      `json:"storageClassName,omitempty"`
}

type pvcResources struct {
	Requests map[string]string `json:"requests"`
}

func buildPVC(in manifestInput) pvc {
	p := pvc{
		APIVersion: "v1",
		Kind:       "PersistentVolumeClaim",
		Metadata: objectMeta{
			Name:        objectName(in.Container),
			Labels:      labels(in.Container),
			Annotations: annotations(in.Container),
		},
		Spec: pvcSpec{
			// One pod at a time by design — the Deployment is Recreate and
			// scales 0 or 1 — so ReadWriteOnce is what every cluster can
			// provide.
			AccessModes: []string{"ReadWriteOnce"},
			Resources:   pvcResources{Requests: map[string]string{"storage": in.Config.StorageSize}},
		},
	}
	if in.Config.StorageClass != "" {
		sc := in.Config.StorageClass
		p.Spec.StorageClassName = &sc
	}
	return p
}

// --- Secret -----------------------------------------------------------------

type secret struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   objectMeta        `json:"metadata"`
	Type       string            `json:"type"`
	StringData map[string]string `json:"stringData"`
}

// buildSecret holds the resolved workspace settings.
//
// stringData rather than data, so the values are not base64-encoded here and
// the API server does that itself. It changes nothing about how exposed they
// are — a Secret is base64, not encryption — but it keeps this code from
// pretending otherwise.
func buildSecret(in manifestInput) secret {
	data := make(map[string]string, len(in.Env)+len(in.Dev.ContainerEnv))
	// The devcontainer.json's own containerEnv first, so a workspace setting of
	// the same name wins — the same precedence the local provider gives.
	for k, v := range in.Dev.ContainerEnv {
		data[k] = v
	}
	for _, e := range in.Env {
		data[e.Key] = e.Value
	}

	return secret{
		APIVersion: "v1",
		Kind:       "Secret",
		Metadata: objectMeta{
			Name:        secretName(in.Container),
			Labels:      labels(in.Container),
			Annotations: annotations(in.Container),
		},
		Type:       "Opaque",
		StringData: data,
	}
}

// --- Deployment ---------------------------------------------------------------

type deployment struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Metadata   objectMeta     `json:"metadata"`
	Spec       deploymentSpec `json:"spec"`
}

type deploymentSpec struct {
	Replicas int             `json:"replicas"`
	Selector labelSelector   `json:"selector"`
	Strategy deploymentStrat `json:"strategy"`
	Template podTemplate     `json:"template"`
}

type labelSelector struct {
	MatchLabels map[string]string `json:"matchLabels"`
}

type deploymentStrat struct {
	Type string `json:"type"`
}

type podTemplate struct {
	Metadata objectMeta `json:"metadata"`
	Spec     podSpec    `json:"spec"`
}

type podSpec struct {
	ServiceAccountName string           `json:"serviceAccountName,omitempty"`
	ImagePullSecrets   []localObjectRef `json:"imagePullSecrets,omitempty"`
	SecurityContext    *podSecurityCtx  `json:"securityContext,omitempty"`
	Containers         []containerSpec  `json:"containers"`
	Volumes            []volume         `json:"volumes"`
}

type localObjectRef struct {
	Name string `json:"name"`
}

type podSecurityCtx struct {
	// FSGroup makes the PVC group-writable by the pod, which is what lets a
	// non-root remoteUser write to a volume the cluster provisions as root.
	FSGroup int `json:"fsGroup"`
}

type containerSpec struct {
	Name            string        `json:"name"`
	Image           string        `json:"image"`
	ImagePullPolicy string        `json:"imagePullPolicy"`
	Command         []string      `json:"command"`
	WorkingDir      string        `json:"workingDir,omitempty"`
	EnvFrom         []envFromSrc  `json:"envFrom,omitempty"`
	VolumeMounts    []volumeMount `json:"volumeMounts"`
}

type envFromSrc struct {
	SecretRef localObjectRef `json:"secretRef"`
}

type volumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
	SubPath   string `json:"subPath,omitempty"`
}

type volume struct {
	Name                  string       `json:"name"`
	PersistentVolumeClaim pvcVolumeSrc `json:"persistentVolumeClaim"`
}

type pvcVolumeSrc struct {
	ClaimName string `json:"claimName"`
}

// defaultFSGroup is an arbitrary non-root group shared by the pod and the
// volume. Any fixed value works; what matters is that it is the same on both.
const defaultFSGroup = 1000

func buildDeployment(in manifestInput) deployment {
	podAnnotations := annotations(in.Container)
	if in.RebuiltAt != "" {
		podAnnotations[annRebuiltAt] = in.RebuiltAt
	}

	d := deployment{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Metadata: objectMeta{
			Name:        objectName(in.Container),
			Labels:      labels(in.Container),
			Annotations: annotations(in.Container),
		},
		Spec: deploymentSpec{
			Replicas: in.Replicas,
			Selector: labelSelector{MatchLabels: labels(in.Container)},
			// Recreate, not RollingUpdate: the PVC is ReadWriteOnce, so a
			// rollout that briefly runs two pods would leave the new one
			// unschedulable waiting for a volume the old one still holds.
			Strategy: deploymentStrat{Type: "Recreate"},
			Template: podTemplate{
				Metadata: objectMeta{
					Labels:      labels(in.Container),
					Annotations: podAnnotations,
				},
				Spec: podSpec{
					ServiceAccountName: in.Config.ServiceAccount,
					SecurityContext:    &podSecurityCtx{FSGroup: defaultFSGroup},
					Containers: []containerSpec{{
						Name:  containerNameMain,
						Image: in.Image,
						// The tag is always :latest, so without this the node
						// keeps serving whatever it cached and a rebuild has no
						// visible effect.
						ImagePullPolicy: "Always",
						// The image's own entrypoint is whatever the project
						// needs at runtime; a devcontainer just has to stay up.
						// A loop rather than `sleep infinity`, which busybox
						// does not accept.
						//
						// The trap is not decoration. This runs as PID 1, and
						// PID 1 ignores signals it has no handler for — so
						// without it a stop waits out the whole termination
						// grace period and only then SIGKILLs. `sleep &` plus
						// `wait` is what lets the trap run: a foreground sleep
						// would not be interrupted.
						Command: []string{"sh", "-c",
							"trap 'exit 0' TERM INT; while :; do sleep 3600 & wait $!; done"},
						WorkingDir:   in.Dev.WorkspaceFolder,
						EnvFrom:      []envFromSrc{{SecretRef: localObjectRef{Name: secretName(in.Container)}}},
						VolumeMounts: mounts(in),
					}},
					Volumes: []volume{{
						Name:                  volumeName,
						PersistentVolumeClaim: pvcVolumeSrc{ClaimName: objectName(in.Container)},
					}},
				},
			},
		},
	}
	if in.Config.ImagePullSecret != "" {
		d.Spec.Template.Spec.ImagePullSecrets = []localObjectRef{{Name: in.Config.ImagePullSecret}}
	}
	return d
}

// --- assembly -------------------------------------------------------------------

// buildManifest renders the three objects as a single List, so one apply
// creates them in one call and either all or none of the command's intent
// reaches the cluster.
func buildManifest(in manifestInput) ([]byte, error) {
	if in.Image == "" {
		return nil, fmt.Errorf("no image for container %s", in.Container.Name)
	}
	if in.Dev.WorkspaceFolder == "" {
		return nil, fmt.Errorf("no workspaceFolder for container %s", in.Container.Name)
	}
	// One mount point nested inside another would mount one subPath over the
	// other, and which one wins is not something to leave to chance. Checked
	// over every pair rather than the one that used to exist, so the state
	// mount cannot shadow the workspace or the home directory either.
	paths := mounts(in)
	for i, a := range paths {
		for _, b := range paths[i+1:] {
			if within(a.MountPath, b.MountPath) || within(b.MountPath, a.MountPath) {
				return nil, fmt.Errorf("mount paths %s and %s overlap", a.MountPath, b.MountPath)
			}
		}
	}

	list := struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Items      []any  `json:"items"`
	}{
		APIVersion: "v1",
		Kind:       "List",
		Items:      []any{buildPVC(in), buildSecret(in), buildDeployment(in)},
	}

	b, err := json.Marshal(list)
	if err != nil {
		return nil, fmt.Errorf("rendering the manifest: %w", err)
	}
	return b, nil
}

// within reports whether child is at or below parent.
func within(child, parent string) bool {
	child, parent = path.Clean(child), path.Clean(parent)
	return child == parent || strings.HasPrefix(child, parent+"/")
}
