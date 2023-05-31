package fileintegrity

import (
	"context"
	"github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type fileIntegrityMapper struct {
	client.Client
}

func (s *fileIntegrityMapper) Map(ctx context.Context, obj client.Object) []reconcile.Request {
	var requests []reconcile.Request

	fiList := v1alpha1.FileIntegrityList{}
	err := s.List(ctx, &fiList, &client.ListOptions{})
	if err != nil {
		return requests
	}

	for _, fi := range fiList.Items {
		// Check for the desired user config, or the default (in the upgrade case).
		if fi.Spec.Config.Name != obj.GetName() && fi.Name != obj.GetName() {
			continue
		}

		if fi.Spec.Config.Namespace != obj.GetNamespace() && fi.Namespace != obj.GetNamespace() {
			continue
		}

		objKey := types.NamespacedName{
			Name:      fi.GetName(),
			Namespace: fi.GetNamespace(),
		}
		requests = append(requests, reconcile.Request{NamespacedName: objKey})
		controllerFileIntegritylog.Info("Will reconcile FI because its config changed",
			"configMap.Name", obj.GetName(), "configMap.Namespace",
			obj.GetNamespace(), "fileIntegrity.Name", fi.Name, "fileIntegrity.Namespace", fi.Namespace)
	}

	return requests
}
