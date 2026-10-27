import pytest
from hypothesis import given
from hypothesis import strategies as st

from forecast import batches


def test_split_cuts_1000_and_keeps_the_tail():
    parts = batches.split(list(range(2500)), 1000)
    assert [len(p) for p in parts] == [1000, 1000, 500]


def test_split_rejects_bad_sizes():
    with pytest.raises(ValueError):
        batches.split([1, 2], 0)
    with pytest.raises(ValueError):
        batches.split([1, 2], 1001)
    with pytest.raises(ValueError):
        batches.split(list(range(batches.MAX_SERIES + 1)), 1000)


@given(n=st.integers(min_value=0, max_value=3000), size=st.integers(min_value=1, max_value=1000))
def test_split_covers_every_series_exactly_once(n, size):
    series = list(range(n))
    parts = batches.split(series, size)
    assert [x for p in parts for x in p] == series
    assert all(len(p) == size for p in parts[:-1])


def test_run_skips_completed_batches_and_does_not_recompute_them():
    calls = []

    def compute(s):
        calls.append(s)
        return s * 2

    outcomes = batches.run(list(range(5)), 2, completed=[0], compute=compute)
    assert [o.status for o in outcomes] == ["skipped", "completed", "completed"]
    assert calls == [2, 3, 4]
    assert outcomes[1].results == [4, 6]
    assert batches.is_complete(outcomes)


def test_failed_batch_is_reported_and_blocks_completion():
    def compute(s):
        if s == 3:
            raise ValueError("history must be non-negative")
        return s

    outcomes = batches.run([0, 1, 2, 3, 4, 5], 2, completed=[], compute=compute)
    assert [o.status for o in outcomes] == ["completed", "failed", "completed"]
    assert outcomes[1].detail == "ValueError: history must be non-negative"
    assert outcomes[1].results == []
    assert not batches.is_complete(outcomes)


def test_resuming_after_a_failure_completes_the_run():
    failing = {"on": True}

    def compute(s):
        if failing["on"] and s == 3:
            raise ValueError("boom")
        return s

    first = batches.run([0, 1, 2, 3], 2, completed=[], compute=compute)
    assert not batches.is_complete(first)
    failing["on"] = False
    done = [o.index for o in first if o.status == "completed"]
    second = batches.run([0, 1, 2, 3], 2, completed=done, compute=compute)
    assert batches.is_complete(second)
    assert [o.status for o in second] == ["skipped", "completed"]


def test_any_compute_error_fails_only_its_batch():
    def compute(s):
        if s == 0:
            raise ZeroDivisionError("division by zero")
        return s

    outcomes = batches.run([0, 1], 1, completed=[], compute=compute)
    assert [o.status for o in outcomes] == ["failed", "completed"]
    assert outcomes[0].detail == "ZeroDivisionError: division by zero"


@given(n=st.integers(min_value=0, max_value=3000), size=st.integers(min_value=1, max_value=1000))
def test_count_matches_split(n, size):
    assert batches.count(n, size) == len(batches.split(list(range(n)), size))
