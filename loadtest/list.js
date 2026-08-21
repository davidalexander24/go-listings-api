// k6 load test for GET /listings — the endpoint whose read path this repo
// makes claims about. Run it before and after a change to the list path; the
// numbers land in docs/benchmarks.md.
//
//   docker run --rm -i -v "${PWD}/loadtest:/scripts" grafana/k6 run /scripts/list.js
//
// Environment:
//   BASE_URL   default http://host.docker.internal:8080
//   VUS        default 50
//   DURATION   default 30s
//   LIMIT      default 50    page size requested
//   PAGES      default 5     how deep to walk the cursor before restarting
//
// Why a cursor walk rather than hammering page 1: page 1 alone would sit
// entirely in cache after the first second and measure Redis, not the read
// path. Walking pages keeps a realistic mix of hits and misses.

import http from 'k6/http';
import { check } from 'k6';
import { Trend, Rate } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://host.docker.internal:8080';
const LIMIT = parseInt(__ENV.LIMIT || '50', 10);
const PAGES = parseInt(__ENV.PAGES || '5', 10);

const pageLatency = new Trend('page_latency', true);
const emptyPages = new Rate('empty_pages');

export const options = {
  vus: parseInt(__ENV.VUS || '50', 10),
  duration: __ENV.DURATION || '30s',
  summaryTrendStats: ['avg', 'min', 'med', 'p(95)', 'p(99)', 'max'],
  thresholds: {
    // Not a pass/fail gate for the benchmark, just a loud marker in the
    // summary if the endpoint is behaving badly.
    http_req_failed: ['rate<0.01'],
  },
};

export default function () {
  let cursor = 0;

  for (let page = 0; page < PAGES; page++) {
    let url = `${BASE_URL}/listings?status=active&category=smartphone&limit=${LIMIT}`;
    if (cursor > 0) {
      url += `&cursor=${cursor}`;
    }

    const res = http.get(url, { tags: { name: 'GET /listings' } });
    pageLatency.add(res.timings.duration);

    const ok = check(res, {
      'status is 200': (r) => r.status === 200,
    });
    if (!ok) {
      return;
    }

    let body;
    try {
      body = res.json();
    } catch (e) {
      return;
    }

    // Tolerates both response shapes so the same script measures the code
    // before and after the pagination envelope lands: a bare array today,
    // {data, next_cursor} afterwards.
    const items = Array.isArray(body) ? body : body.data;
    if (!items || items.length === 0) {
      emptyPages.add(true);
      return;
    }
    emptyPages.add(false);

    if (!Array.isArray(body) && body.next_cursor != null) {
      cursor = body.next_cursor;
    } else {
      // Bare-array shape: derive the cursor the way a client is forced to.
      cursor = items[items.length - 1].id;
    }

    if (items.length < LIMIT) {
      return; // ran out of rows
    }
  }
}
