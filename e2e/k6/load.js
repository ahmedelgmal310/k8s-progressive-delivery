// Steady load against demo-api for the whole e2e run.
// noConnectionReuse makes kube-proxy balance per request, so the share of
// traffic that reaches the canary matches its share of pods.
import http from 'k6/http';

const target = __ENV.TARGET || 'http://demo-api.demo.svc.cluster.local/';

export const options = {
  noConnectionReuse: true,
  discardResponseBodies: true,
  summaryTrendStats: ['avg', 'p(95)', 'max'],
  scenarios: {
    steady: {
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.RATE || 40),
      timeUnit: '1s',
      duration: __ENV.DURATION || '30m',
      preAllocatedVUs: 20,
      maxVUs: 100,
    },
  },
};

export default function () {
  http.get(target, { timeout: '5s' });
}
