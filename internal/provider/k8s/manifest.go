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

const (
	// annRebuiltAt changes on every rebuild. The image tag never does, so only
	// a changed pod template starts a new ReplicaSet.
	annRebuiltAt = "dev.rebuilt-at"
)

// Home must persist: scaling to 0 gives a fresh container filesystem, so
// anything postCreate wrote to ~ would be lost on every stop.
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

// mounts is what the pod mounts off the claim. Only PersistState is read from
// the container: its generated volume is a docker volume, meaningless to a
// cluster.
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
			// ReadWriteOnce is the one mode every cluster provides; the
			// Deployment runs one pod.
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

// buildSecret holds the resolved workspace settings. stringData, so the API
// server does the base64 encoding.
func buildSecret(in manifestInput) secret {
	data := make(map[string]string, len(in.Env)+len(in.Dev.ContainerEnv))
	// containerEnv first, so a workspace setting of the same name wins, as on
	// local.
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
	// FSGroup makes the PVC group-writable, so a non-root remoteUser can write
	// to it.
	FSGroup int `json:"fsGroup"`
	// A pod runs unconfined unless asked, so RuntimeDefault matches local
	// containers.
	SeccompProfile seccompProfile `json:"seccompProfile"`
}

type seccompProfile struct {
	Type string `json:"type"`
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

// defaultFSGroup is the group shared by the pod and its volume. Any value
// works, as long as both sides use it.
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
			// Recreate, not RollingUpdate: a second pod could not mount the
			// ReadWriteOnce PVC.
			Strategy: deploymentStrat{Type: "Recreate"},
			Template: podTemplate{
				Metadata: objectMeta{
					Labels:      labels(in.Container),
					Annotations: podAnnotations,
				},
				Spec: podSpec{
					ServiceAccountName: in.Config.ServiceAccount,
					SecurityContext: &podSecurityCtx{
						FSGroup:        defaultFSGroup,
						SeccompProfile: seccompProfile{Type: "RuntimeDefault"},
					},
					Containers: []containerSpec{{
						Name:  containerNameMain,
						Image: in.Image,
						// The tag is always :latest, so without Always a node
						// serves its cached image.
						ImagePullPolicy: "Always",
						// Loop, not `sleep infinity`, which busybox rejects.
						//
						// This is PID 1, which ignores signals without a
						// handler, so the trap is what lets a stop finish
						// before the grace period ends. The backgrounded sleep
						// and wait let the trap run mid-sleep.
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

// buildManifest renders the three objects as one List, applied in one call.
func buildManifest(in manifestInput) ([]byte, error) {
	if in.Image == "" {
		return nil, fmt.Errorf("no image for container %s", in.Container.Name)
	}
	if in.Dev.WorkspaceFolder == "" {
		return nil, fmt.Errorf("no workspaceFolder for container %s", in.Container.Name)
	}
	// A nested mount point shadows its parent's subPath; check every pair,
	// state included.
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
