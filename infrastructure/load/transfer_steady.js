import http from 'k6/http';
import { check, sleep } from 'k6';
import { uuidv4 } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js';

export const options = {
  vus: 50,
  duration: '5m',
  thresholds: {
    http_req_duration: ['p(95)<200', 'p(99)<500'],
    http_req_failed: ['rate<0.001'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const TOKEN = __ENV.JWT_TOKEN || 'replace-with-valid-jwt';

export default function () {
  const res = http.post(
    `${BASE_URL}/transfers`,
    JSON.stringify({
      sourceWalletId: __ENV.SOURCE_WALLET_ID,
      destinationWalletId: __ENV.DEST_WALLET_ID,
      amount: '1.00',
    }),
    {
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${TOKEN}`,
        'Idempotency-Key': uuidv4(),
      },
    }
  );

  check(res, {
    'status 201': (r) => r.status === 201,
  });

  sleep(0.02);
}
