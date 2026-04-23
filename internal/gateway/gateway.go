package gateway

import (
	"encoding/json"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/dbaas/api/v1alpha1"
)

func RunGateway(addr string, k8sClient client.Client) error {
	auth := newAuthMiddleware(k8sClient, "dbaas-api-keys", "dbaas-system")

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/dbinstances", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleListInstances(w, r, k8sClient)
		case http.MethodPost:
			handleCreateInstance(w, r, k8sClient)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	})
	mux.HandleFunc("/dbinstances/", func(w http.ResponseWriter, r *http.Request) {
		handleInstanceRoute(w, r, k8sClient)
	})

	return http.ListenAndServe(addr, auth.Wrap(mux))
}

func handleListInstances(w http.ResponseWriter, r *http.Request, k8sClient client.Client) {
	if !RequireRole(w, r, RoleAdmin, RoleOperator, RoleViewer) {
		return
	}
	var instances dbaasv1.DBInstanceList
	if err := k8sClient.List(r.Context(), &instances); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, instances)
}

func handleCreateInstance(w http.ResponseWriter, r *http.Request, k8sClient client.Client) {
	if !RequireRole(w, r, RoleAdmin) {
		return
	}
	defer r.Body.Close()

	var instance dbaasv1.DBInstance
	if err := json.NewDecoder(r.Body).Decode(&instance); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if instance.Name == "" {
		writeError(w, http.StatusBadRequest, "metadata.name is required")
		return
	}
	if instance.APIVersion == "" {
		instance.APIVersion = dbaasv1.GroupVersion.String()
	}
	if instance.Kind == "" {
		instance.Kind = "DBInstance"
	}
	if err := k8sClient.Create(r.Context(), &instance); err != nil {
		if apierrors.IsAlreadyExists(err) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, instance)
}

func handleInstanceRoute(w http.ResponseWriter, r *http.Request, k8sClient client.Client) {
	path := strings.TrimPrefix(r.URL.Path, "/dbinstances/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "instance name is required")
		return
	}

	name := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			handleGetInstance(w, r, k8sClient, name)
		case http.MethodDelete:
			handleDeleteInstance(w, r, k8sClient, name)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	switch parts[1] {
	case "start":
		handleSetRunning(w, r, k8sClient, name, true)
	case "stop":
		handleSetRunning(w, r, k8sClient, name, false)
	default:
		writeError(w, http.StatusNotFound, "unsupported action")
	}
}

func handleGetInstance(w http.ResponseWriter, r *http.Request, k8sClient client.Client, name string) {
	if !RequireRole(w, r, RoleAdmin, RoleOperator, RoleViewer) {
		return
	}
	var instance dbaasv1.DBInstance
	if err := k8sClient.Get(r.Context(), types.NamespacedName{Name: name}, &instance); err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, instance)
}

func handleDeleteInstance(w http.ResponseWriter, r *http.Request, k8sClient client.Client, name string) {
	if !RequireRole(w, r, RoleAdmin) {
		return
	}
	instance := &dbaasv1.DBInstance{}
	instance.Name = name
	instance.APIVersion = dbaasv1.GroupVersion.String()
	instance.Kind = "DBInstance"

	if err := k8sClient.Delete(r.Context(), instance); err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "deletion requested", "name": name})
}

func handleSetRunning(w http.ResponseWriter, r *http.Request, k8sClient client.Client, name string, running bool) {
	if !RequireRole(w, r, RoleAdmin, RoleOperator) {
		return
	}
	var instance dbaasv1.DBInstance
	if err := k8sClient.Get(r.Context(), types.NamespacedName{Name: name}, &instance); err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	instance.Spec.Running = boolPtr(running)
	if err := k8sClient.Update(r.Context(), &instance); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, instance)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func boolPtr(v bool) *bool {
	return &v
}
