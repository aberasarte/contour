// Copyright Project Contour Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dag

import (
	"strings"

	"github.com/projectcontour/contour/internal/annotation"
	"github.com/projectcontour/contour/internal/k8s"
	"k8s.io/api/networking/v1beta1"
	"k8s.io/apimachinery/pkg/types"
)

// IngressProcessor translates Ingresses into DAG
// objects and adds them to the DAG builder.
type IngressProcessor struct {
	builder *Builder
}

// Run translates Ingresses into DAG objects and
// adds them to the DAG builder.
func (p *IngressProcessor) Run(builder *Builder) {
	p.builder = builder

	// reset the processor when we're done
	defer func() {
		p.builder = nil
	}()

	// setup secure vhosts if there is a matching secret
	// we do this first so that the set of active secure vhosts is stable
	// during computeIngresses.
	p.computeSecureVirtualhosts()
	p.computeIngresses()
}

// computeSecureVirtualhosts populates tls parameters of
// secure virtual hosts.
func (p *IngressProcessor) computeSecureVirtualhosts() {
	for _, ing := range p.builder.Source.ingresses {
		for _, tls := range ing.Spec.TLS {
			secretName := k8s.NamespacedNameFrom(tls.SecretName, k8s.DefaultNamespace(ing.GetNamespace()))
			sec, err := p.builder.Source.LookupSecret(secretName, validSecret)
			if err != nil {
				p.builder.WithError(err).
					WithField("name", ing.GetName()).
					WithField("namespace", ing.GetNamespace()).
					WithField("secret", secretName).
					Error("unresolved secret reference")
				continue
			}

			if !p.builder.Source.DelegationPermitted(secretName, ing.GetNamespace()) {
				p.builder.WithError(err).
					WithField("name", ing.GetName()).
					WithField("namespace", ing.GetNamespace()).
					WithField("secret", secretName).
					Error("certificate delegation not permitted")
				continue
			}

			// We have validated the TLS secrets, so we can go
			// ahead and create the SecureVirtualHost for this
			// Ingress.
			for _, host := range tls.Hosts {
				svhost := p.builder.lookupSecureVirtualHost(host)
				svhost.Secret = sec
				svhost.MinTLSVersion = annotation.MinTLSVersion(
					annotation.CompatAnnotation(ing, "tls-minimum-protocol-version"))
			}
		}
	}
}

func (p *IngressProcessor) computeIngresses() {
	// deconstruct each ingress into routes and virtualhost entries
	for _, ing := range p.builder.Source.ingresses {

		// rewrite the default ingress to a stock ingress rule.
		rules := rulesFromSpec(ing.Spec)
		for _, rule := range rules {
			p.computeIngressRule(ing, rule)
		}
	}
}

func (p *IngressProcessor) computeIngressRule(ing *v1beta1.Ingress, rule v1beta1.IngressRule) {
	host := rule.Host
	if strings.Contains(host, "*") {
		// reject hosts with wildcard characters.
		return
	}
	if host == "" {
		// if host name is blank, rewrite to Envoy's * default host.
		host = "*"
	}
	for _, httppath := range httppaths(rule) {
		path := stringOrDefault(httppath.Path, "/")
		be := httppath.Backend
		m := types.NamespacedName{Name: be.ServiceName, Namespace: ing.Namespace}
		s, err := p.builder.lookupService(m, be.ServicePort)
		if err != nil {
			continue
		}

		r := route(ing, path, s)

		// should we create port 80 routes for this ingress
		if annotation.TLSRequired(ing) || annotation.HTTPAllowed(ing) {
			p.builder.lookupVirtualHost(host).addRoute(r)
		}

		// computeSecureVirtualhosts will have populated b.securevirtualhosts
		// with the names of tls enabled ingress objects. If host exists then
		// it is correctly configured for TLS.
		svh, ok := p.builder.securevirtualhosts[host]
		if ok && host != "*" {
			svh.addRoute(r)
		}
	}
}

// route builds a dag.Route for the supplied Ingress.
func route(ingress *v1beta1.Ingress, path string, service *Service) *Route {
	wr := annotation.WebsocketRoutes(ingress)
	r := &Route{
		HTTPSUpgrade:  annotation.TLSRequired(ingress),
		Websocket:     wr[path],
		TimeoutPolicy: ingressTimeoutPolicy(ingress),
		RetryPolicy:   ingressRetryPolicy(ingress),
		Clusters: []*Cluster{{
			Upstream: service,
			Protocol: service.Protocol,
		}},
	}

	if strings.ContainsAny(path, "^+*[]%") {
		// path smells like a regex
		r.PathMatchCondition = &RegexMatchCondition{Regex: path}
		return r
	}

	r.PathMatchCondition = &PrefixMatchCondition{Prefix: path}
	return r
}

// rulesFromSpec merges the IngressSpec's Rules with a synthetic
// rule representing the default backend.
func rulesFromSpec(spec v1beta1.IngressSpec) []v1beta1.IngressRule {
	rules := spec.Rules
	if backend := spec.Backend; backend != nil {
		rule := defaultBackendRule(backend)
		rules = append(rules, rule)
	}
	return rules
}

// defaultBackendRule returns an IngressRule that represents the IngressBackend.
func defaultBackendRule(be *v1beta1.IngressBackend) v1beta1.IngressRule {
	return v1beta1.IngressRule{
		IngressRuleValue: v1beta1.IngressRuleValue{
			HTTP: &v1beta1.HTTPIngressRuleValue{
				Paths: []v1beta1.HTTPIngressPath{{
					Backend: v1beta1.IngressBackend{
						ServiceName: be.ServiceName,
						ServicePort: be.ServicePort,
					},
				}},
			},
		},
	}
}

func stringOrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// httppaths returns a slice of HTTPIngressPath values for a given IngressRule.
// In the case that the IngressRule contains no valid HTTPIngressPaths, a
// nil slice is returned.
func httppaths(rule v1beta1.IngressRule) []v1beta1.HTTPIngressPath {
	if rule.IngressRuleValue.HTTP == nil {
		// rule.IngressRuleValue.HTTP value is optional.
		return nil
	}
	return rule.IngressRuleValue.HTTP.Paths
}
