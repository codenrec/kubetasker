//go:build e2e
// +build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kndclark/kubetasker/test/utils"
)

const (
	promNamespace   = "monitoring"
	promReleaseName = "e2e-prom"

	kubetaskerNS  = "kubetasker-metrics-e2e"
	kubetaskerRel = "kubetasker-metrics-e2e"
	promPort      = 19090 // local port for port-forward
)

var promPortForward *exec.Cmd

// promTargetsResponse maps /api/v1/targets response
type promTargetsResponse struct {
	Status string `json:"status"`
	Data   struct {
		ActiveTargets []struct {
			Labels map[string]string `json:"labels"`
			Health string            `json:"health"`
		} `json:"activeTargets"`
	} `json:"data"`
}

func deployPrometheus() {
	By("adding prometheus helm repo (ignore if exists)")
	_, _ = utils.Run(exec.Command(
		"helm", "repo", "add",
		"prometheus-community",
		"https://prometheus-community.github.io/helm-charts",
	))

	By("updating helm repos")
	out, err := utils.Run(exec.Command("helm", "repo", "update"))
	Expect(err).NotTo(HaveOccurred(), out)

	By("installing kube-prometheus-stack")
	out, err = utils.Run(exec.Command(
		"helm", "upgrade", "--install",
		promReleaseName,
		"prometheus-community/kube-prometheus-stack",
		"--namespace", promNamespace,
		"--create-namespace",
		"--set", "grafana.enabled=false",
		"--set", "alertmanager.enabled=false",
	))
	Expect(err).NotTo(HaveOccurred(), out)

	By("waiting for Prometheus pods to be ready")
	Eventually(func() error {
		_, err := utils.Run(exec.Command(
			"kubectl", "wait",
			"--namespace", promNamespace,
			"--for=condition=Ready",
			"pods",
			"-l", "app.kubernetes.io/name=prometheus",
			"--timeout=5s",
		))
		return err
	}, 3*time.Minute, 5*time.Second).Should(Succeed())
}

func startPrometheusPortForward() {
	By("starting Prometheus port-forward")
	promPortForward = exec.Command(
		"kubectl", "port-forward",
		"-n", promNamespace,
		fmt.Sprintf("svc/%s-kube-prometheus-s-prometheus", promReleaseName),
		fmt.Sprintf("%d:9090", promPort),
	)
	Expect(promPortForward.Start()).To(Succeed())
	time.Sleep(3 * time.Second) // allow port-forward to bind
}

func stopPrometheusPortForward() {
	if promPortForward != nil && promPortForward.Process != nil {
		_ = promPortForward.Process.Kill()
		_, _ = promPortForward.Process.Wait()
	}
}

func expectJobs(jobs ...string) {
	Eventually(func() error {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%d/api/v1/targets", promPort))
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		var parsed promTargetsResponse
		if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
			return err
		}

		for _, job := range jobs {
			found := false
			for _, t := range parsed.Data.ActiveTargets {
				if t.Labels["job"] == job && t.Health == "up" {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("job %s not found/up yet", job)
			}
		}
		return nil
	}, 3*time.Minute, 5*time.Second).Should(Succeed())
}

func queryMetric(metric string) string {
	var body string
	Eventually(func() error {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%d/api/v1/query?query=%s", promPort, metric))
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		body = string(b)
		if body == "" || (!containsSuccessStatus(body) && !containsResult(body)) {
			return fmt.Errorf("metric %s not available yet", metric)
		}
		return nil
	}, 2*time.Minute, 5*time.Second).Should(Succeed())
	return body
}

func containsSuccessStatus(body string) bool {
	return body != "" && (stringContains(body, `"status":"success"`))
}

func containsResult(body string) bool {
	return body != "" && stringContains(body, `"result"`)
}

func stringContains(s, substr string) bool {
	return len(s) >= len(substr) && (len(substr) == 0 || (len(s) > 0 && len(substr) > 0 && strings.Contains(s, substr)))
}

var _ = Describe("Prometheus metrics scraping", Ordered, func() {

	BeforeAll(func() {
		deployPrometheus()
		startPrometheusPortForward()
	})

	AfterAll(func() {
		stopPrometheusPortForward()
		By("uninstalling Prometheus")
		_, _ = utils.Run(exec.Command(
			"helm", "uninstall",
			promReleaseName,
			"-n", promNamespace,
		))
	})

	It("scrapes controller and frontend metrics", func() {
		expectJobs(
			kubetaskerRel+"-kubetasker-controller",
			kubetaskerRel+"-kubetasker-frontend",
		)
		controllerMetric := queryMetric("kubetasker_reconcile_total")
		frontendMetric := queryMetric("kubetasker_frontend_requests_total")

		GinkgoWriter.Println("Controller metric:", controllerMetric)
		GinkgoWriter.Println("Frontend metric:", frontendMetric)
	})
})
