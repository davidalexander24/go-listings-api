// k6 load test for GET /listings/{id} — the single-item read that the Redis
// cache-aside layer exists to serve.
//
//   docker run --rm -i -v "${PWD}/loadtest:/scripts" grafana/k6 run /scripts/get.js
//
// Environment:
//   BASE_URL   default http://host.docker.internal:8080
//   VUS        default 50
//   DURATION   default 30s
//   MAX_ID     default 1000000   upper bound of the seeded id range
//   HOT_RATIO  default 0.8       share of requests aimed at the hot set
//   HOT_SIZE   default 1000      how many ids count as hot
//
// Why a hot set rather than uniform random ids: uniform access over a million
// rows gives a cache hit rate near zero, which measures Postgres and tells you
// nothing about the cache. Real marketplace traffic is heavily skewed, so the
// default sends 80% of reads at 1,000 ids and the rest across the whole range.

import http from 'k6/http';
import { check } from 'k6';
import { Rate } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://host.docker.internal:8080';
const MAX_ID = parseInt(__ENV.MAX_ID || '1000000', 10);
const HOT_RATIO = parseFloat(__ENV.HOT_RATIO || '0.8');
const HOT_SIZE = parseInt(__ENV.HOT_SIZE || '1000', 10);

const notFound = new Rate('not_found');

export const options = {
  vus: parseInt(__ENV.VUS || '50', 10),
  duration: __ENV.DURATION || '30s',
  summaryTrendStats: ['avg', 'min', 'med', 'p(95)', 'p(99)', 'max'],
  thresholds: {
    http_req_failed: ['rate<0.01'],
  },
};

export default function () {
  let id;
  if (Math.random() < HOT_RATIO) {
    // Hot set: the newest ids, which is where marketplace attention actually
    // goes.
    id = MAX_ID - Math.floor(Math.random() * HOT_SIZE);
  } else {
    id = 1 + Math.floor(Math.random() * MAX_ID);
  }

  const res = http.get(`${BASE_URL}/listings/${id}`, { tags: { name: 'GET /listings/{id}' } });

  notFound.add(res.status === 404);

  check(res, {
    'status is 200 or 404': (r) => r.status === 200 || r.status === 404,
  });
}
