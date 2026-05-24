import http from 'k6/http';
import { check } from 'k6';
import { uuidv4 } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js';

export const options = {
  stages: [
    { duration: '30s', target: 10 },
    { duration: '30s', target: 200 },
    { duration: '1m', target: 200 },
    { duration: '30s', target: 0 },
  ],
  thresholds: {
    http_req_duration: ['p(99)<5000'],
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
    'not 5xx': (r) => r.status < 500,
  });
}
