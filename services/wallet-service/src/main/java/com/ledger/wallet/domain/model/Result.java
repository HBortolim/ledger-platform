package com.ledger.wallet.domain.model;

import java.util.function.Function;

public sealed interface Result<S, F> permits Result.Success, Result.Failure {

    record Success<S, F>(S value) implements Result<S, F> {}
    record Failure<S, F>(F error) implements Result<S, F> {}

    static <S, F> Result<S, F> success(S value) {
        return new Success<>(value);
    }

    static <S, F> Result<S, F> failure(F error) {
        return new Failure<>(error);
    }

    default <T> T fold(Function<S, T> onSuccess, Function<F, T> onFailure) {
        return switch (this) {
            case Success<S, F>(S value) -> onSuccess.apply(value);
            case Failure<S, F>(F error) -> onFailure.apply(error);
        };
    }

    default S orElseThrow(Function<F, ? extends RuntimeException> mapper) {
        return switch (this) {
            case Success<S, F>(S value) -> value;
            case Failure<S, F>(F error) -> throw mapper.apply(error);
        };
    }
}
