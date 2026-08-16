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
