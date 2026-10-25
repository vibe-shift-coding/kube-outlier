package connect

// kubectl still accepts the legacy "auth-provider: oidc" kubeconfig entry, so
// kube-outlier does too. Exec credential plugins (aws eks get-token,
// gke-gcloud-auth-plugin, kubelogin) need no import; the gcp and azure auth
// providers were removed from client-go in favour of those plugins.
import _ "k8s.io/client-go/plugin/pkg/client/auth/oidc"
