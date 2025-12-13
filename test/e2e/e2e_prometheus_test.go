//go:build e2e
// +build e2e

package e2e

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kndclark/kubetasker/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

const (
	prometheusPort = 9090
)

var _ = Describe("Prometheus Integration", Ordered, func() {
	const (
		namespace          = "kubetasker-prometheus-e2e"
		releaseName        = "kubetasker-prometheus"
		controllerFullName = releaseName + "-kubetasker-controller"
		frontendFullName   = releaseName + "-kubetasker-frontend"
		monitoringNS       = "monitoring"
		prometheusSvcName  = "prometheus" // The service name within the kube-prometheus-stack chart
	)

	BeforeAll(func() {
		// This test suite is responsible for installing its own Prometheus dependency.
		prometheusReleaseName := os.Getenv("PROMETHEUS_RELEASE_NAME")
		Expect(prometheusReleaseName).NotTo(BeEmpty(), "PROMETHEUS_RELEASE_NAME env var must be set")

		By("adding and updating the prometheus-community helm repository")
		cmd := exec.Command("helm", "repo", "add", "prometheus-community", "https://prometheus-community.github.io/helm-charts", "--force-update")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to add prometheus-community helm repo")
		cmd = exec.Command("helm", "repo", "update")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to update helm repos")

		By("installing the kube-prometheus-stack")
		cmd = exec.Command("helm", "install", prometheusReleaseName, "prometheus-community/kube-prometheus-stack",
			"--namespace", monitoringNS, "--create-namespace", "--wait")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install kube-prometheus-stack. The command may have timed out.")

		By("creating the test namespace")
		cmd = exec.Command("kubectl", "create", "ns", namespace)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("deploying the KubeTasker umbrella chart")
		umbrellaChartPath := filepath.Join(chartsRoot, "kubetasker")
		cmd = exec.Command("helm", "install", releaseName, umbrellaChartPath,
			"--namespace", namespace,
			"--set", fmt.Sprintf("kubetasker-controller.image.repository=%s", strings.Split(projectImage, ":")[0]),
			"--set", fmt.Sprintf("kubetasker-controller.image.tag=%s", strings.Split(projectImage, ":")[1]),
			"--set", fmt.Sprintf("kubetasker-frontend.image.repository=%s", strings.Split(frontendImage, ":")[0]),
			"--set", fmt.Sprintf("kubetasker-frontend.image.tag=%s", strings.Split(frontendImage, ":")[1]),
			"--set", "global.imagePullPolicy=IfNotPresent",
			"--set", "kubetasker-controller.prometheus.serviceMonitor.enabled=true",
			"--set", "kubetasker-frontend.prometheus.serviceMonitor.enabled=true",
			"--set", "kubetasker-controller.fullnameOverride="+controllerFullName,
			"--set", "kubetasker-frontend.fullnameOverride="+frontendFullName,
			"--set", "kubetasker-controller.webhook.service.namespace="+namespace,
			"--set", "kubetasker-controller.prometheus.serviceMonitor.labels.release=ktask-prometheus-stack",
			"--set", "kubetasker-frontend.prometheus.serviceMonitor.labels.release=ktask-prometheus-stack",
			"--timeout", "3m",
			"--wait")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy KubeTasker umbrella chart")

		// The `helm install --wait` command makes the following checks redundant.
		// If Helm installation succeeds, the pods are ready.
		By("all pods are ready due to 'helm install --wait'")
	})

	AfterAll(func() {
		By("cleaning up the Prometheus test environment")
		prometheusReleaseName := os.Getenv("PROMETHEUS_RELEASE_NAME")
		if prometheusReleaseName != "" {
			By("uninstalling the kube-prometheus-stack")
			_, _ = utils.Run(exec.Command("helm", "uninstall", prometheusReleaseName, "-n", monitoringNS, "--ignore-not-found"))
			_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", monitoringNS, "--ignore-not-found"))
		}
		_, _ = utils.Run(exec.Command("helm", "uninstall", releaseName, "-n", namespace, "--ignore-not-found"))
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", namespace, "--ignore-not-found"))
		cleanupWebhookConfigurations(controllerFullName)
	})

	It("should have its controller and frontend metrics scraped by Prometheus", func() {
		// Dynamically construct the prometheus service name from the environment variable
		// set in the Makefile. This is more robust than hardcoding.
		prometheusReleaseName := os.Getenv("PROMETHEUS_RELEASE_NAME")
		Expect(prometheusReleaseName).NotTo(BeEmpty(), "PROMETHEUS_RELEASE_NAME env var must be set")
		prometheusSvc := fmt.Sprintf("%s-%s", prometheusReleaseName, prometheusSvcName)
		By("starting port-forward to Prometheus")
		// Use gexec.Start to manage the port-forward process in the background.
		// It's more robust than managing context and pipes manually.
		portForwardCmd := exec.Command("kubectl", "port-forward", "-n", monitoringNS, fmt.Sprintf("svc/%s", prometheusSvc), fmt.Sprintf("%d:9090", prometheusPort))
		portForwardSession, err := gexec.Start(portForwardCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		// Ensure the port-forward process is killed at the end of the test.
		defer portForwardSession.Kill()

		// A more reliable way to wait for port-forward is to poll the local port.
		// This avoids parsing kubectl output, which can be brittle.
		Eventually(func() error {
			resp, err := http.Get(fmt.Sprintf("http://localhost:%d", prometheusPort))
			if err == nil {
				resp.Body.Close()
			}
			return err
		}, "30s", "1s").Should(Succeed(), "Port-forward to Prometheus did not become ready")

		// Use a direct PromQL query to check if the target is up. This is more efficient
		// than fetching all targets and iterating through them.
		// The job name format is <namespace>/<service-name>/<endpoint-port-name-or-index>
		controllerJob := fmt.Sprintf("%s/%s-metrics-service/0", namespace, controllerFullName)
		frontendJob := fmt.Sprintf("%s/%s-metrics-service/0", namespace, frontendFullName)

		By("verifying controller metrics are scraped")
		Eventually(func(g Gomega) {
			// The 'up' metric is a fundamental health check in Prometheus.
			// A value of 1 means the target is successfully scraped.
			promQL := fmt.Sprintf(`up{job="%s"}`, controllerJob)
			output, err := queryPrometheus(promQL)
			g.Expect(err).NotTo(HaveOccurred())
			// The response for a successful 'up' query will contain `"value":[<timestamp>,"1"]`
			g.Expect(output).To(ContainSubstring(`"value":[`), "Expected a value in prometheus response")
			g.Expect(output).To(ContainSubstring(`,"1"]`), "Expected target to be up (1)")
		}, "3m", "10s").Should(Succeed(), "Controller metrics should be scraped and up")

		By("verifying frontend metrics are scraped")
		Eventually(func(g Gomega) {
			promQL := fmt.Sprintf(`up{job="%s"}`, frontendJob)
			output, err := queryPrometheus(promQL)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(ContainSubstring(`"value":[`), "Expected a value in prometheus response")
			g.Expect(output).To(ContainSubstring(`,"1"]`), "Expected target to be up (1)")
		}, "3m", "10s").Should(Succeed(), "Frontend metrics should be scraped and up")
	})
})

// queryPrometheus is a helper to query the Prometheus API.
func queryPrometheus(query string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", fmt.Sprintf("http://localhost:%d/api/v1/query", prometheusPort), nil)
	if err != nil {
		return "", err
	}

	q := req.URL.Query()
	q.Add("query", query)
	req.URL.RawQuery = q.Encode()

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to query Prometheus: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Prometheus query returned non-200 status: %s", resp.Status)
	}

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return "", fmt.Errorf("failed to read Prometheus response body: %w", err)
	}

	return strings.TrimSpace(buf.String()), nil
}
