package com.ledger.wallet.api.filter;

import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import jakarta.servlet.FilterChain;
import jakarta.servlet.ServletException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;
import org.springframework.stereotype.Component;
import org.springframework.web.filter.OncePerRequestFilter;
import org.springframework.web.servlet.HandlerMapping;

import java.io.IOException;
import java.util.concurrent.TimeUnit;

/**
 * SPEC.md §7.3: wallet_requests_total / wallet_request_duration_seconds. Registered as a plain
 * servlet filter (default order), so it wraps the dispatch to the controller and observes the
 * final response status set by GlobalExceptionHandler — but runs after the Spring Security chain
 * (JwtAuthFilter), so a request rejected there never reaches this filter.
 *
 * <p>This means {@code wallet_requests_total} can never carry a {@code status="401"} (or other
 * security-rejection) observation. That gap is an accepted tradeoff, not an oversight: reordering
 * this filter ahead of Spring Security would be a behavior change requiring new test coverage, and
 * the gap is already mitigated by Micrometer's default HTTP metrics (kept per this task's Global
 * Constraints). Spring Boot registers its {@code ServerHttpObservationFilter} — the producer of
 * {@code http.server.requests} / {@code http_server_requests_seconds} — at {@code
 * Ordered.HIGHEST_PRECEDENCE + 1} (see {@code WebMvcObservationAutoConfiguration}), which runs
 * outside and before Spring Security's chain ({@code SecurityProperties.DEFAULT_FILTER_ORDER =
 * -100}). So auth rejections, including 401s from {@code JwtAuthFilter}, remain observable via
 * {@code http_server_requests_seconds} even though they never reach {@code wallet_requests_total}.
 */
@Component
public class MetricsFilter extends OncePerRequestFilter {

    private final MeterRegistry meterRegistry;

    public MetricsFilter(MeterRegistry meterRegistry) {
        this.meterRegistry = meterRegistry;
    }

    @Override
    protected void doFilterInternal(
            HttpServletRequest request,
            HttpServletResponse response,
            FilterChain chain
    ) throws ServletException, IOException {
        long startNanos = System.nanoTime();
        try {
            chain.doFilter(request, response);
        } finally {
            String endpoint = endpointLabel(request);
            String method = request.getMethod();
            String status = String.valueOf(response.getStatus());

            meterRegistry.counter("wallet_requests", "endpoint", endpoint, "method", method, "status", status)
                    .increment();
            Timer.builder("wallet_request_duration")
                    .tag("endpoint", endpoint)
                    .tag("method", method)
                    .publishPercentileHistogram()
                    .register(meterRegistry)
                    .record(System.nanoTime() - startNanos, TimeUnit.NANOSECONDS);
        }
    }

    private String endpointLabel(HttpServletRequest request) {
        Object pattern = request.getAttribute(HandlerMapping.BEST_MATCHING_PATTERN_ATTRIBUTE);
        return pattern != null ? pattern.toString() : "UNMATCHED";
    }
}
