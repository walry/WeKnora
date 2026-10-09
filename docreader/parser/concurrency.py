import logging
import threading
from contextlib import contextmanager
from typing import Dict, Iterator

from docreader.config import CONFIG

logger = logging.getLogger(__name__)

_LIMITERS: Dict[str, threading.BoundedSemaphore] = {}
_LIMITERS_LOCK = threading.Lock()

# pdfium (pypdfium2) is initialised with m_pIsolate=None and has no internal
# locking, so concurrent calls from multiple threads race on pdfium's shared
# global state and cause intermittent parse failures/crashes. docreader serves
# parses from a gRPC thread pool, so all in-process pdfium access must be
# serialised through this process-wide, reentrant lock (reentrant so the
# PDFParser -> PDFScannedParser fallback can nest).
_PDFIUM_LOCK = threading.RLock()


def _get_limiter(name: str, max_workers: int) -> threading.BoundedSemaphore:
    with _LIMITERS_LOCK:
        limiter = _LIMITERS.get(name)
        if limiter is None:
            limiter = threading.BoundedSemaphore(max_workers)
            _LIMITERS[name] = limiter
        return limiter


@contextmanager
def parser_worker_limit(name: str, max_workers: int) -> Iterator[None]:
    """Limit concurrent access to heavy, process-wide parser operations.

    Set max_workers <= 0 to disable throttling for deployments that have enough
    CPU/GPU resources and know the parser backend is safe under concurrency.
    """

    if max_workers <= 0:
        yield
        return

    limiter = _get_limiter(name, max_workers)
    logger.debug("Waiting for %s parser slot (max_workers=%d)", name, max_workers)
    limiter.acquire()
    try:
        yield
    finally:
        limiter.release()


@contextmanager
def pdfium_lock() -> Iterator[None]:
    """Serialise all in-process pdfium (pypdfium2) access.

    pdfium is not thread-safe, so every code path that touches it from the gRPC
    thread pool must hold this lock. Reentrant, so nested acquisition within one
    parse is safe. Set ``DOCREADER_PDFIUM_SERIALIZE=false`` to disable for a
    thread-safe pdfium build.
    """
    if not CONFIG.pdfium_serialize:
        yield
        return

    _PDFIUM_LOCK.acquire()
    try:
        yield
    finally:
        _PDFIUM_LOCK.release()


def _select_mp_context():
    """Pick the safest available multiprocessing start method.

    ``forkserver`` forks workers from a clean, single-threaded server process,
    avoiding the fork-in-a-multithreaded-process hazards of the gRPC server.
    Falls back to ``fork`` and finally returns ``None`` (serial) when neither
    is available (e.g. Windows/dev).
    """
    import multiprocessing as mp

    for method in ("forkserver", "fork"):
        try:
            return mp.get_context(method)
        except ValueError:
            continue
    return None
