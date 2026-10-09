import base64
import contextlib
import io
import threading
import time
import unittest
import uuid
from unittest import mock

from PIL import Image

from docreader.config import CONFIG
from docreader.parser import pdf_parser
from docreader.parser.concurrency import parser_worker_limit, pdfium_lock
from docreader.parser.pdf_parser import PDFParser, PDFScannedParser, _normalize_image_quality


def _make_scanned_pdf(pages: int = 2, size=(64, 64)) -> bytes:
    buf = io.BytesIO()
    imgs = [Image.new("RGB", size, "white") for _ in range(pages)]
    imgs[0].save(buf, format="PDF", save_all=True, append_images=imgs[1:])
    return buf.getvalue()


class ParserConcurrencyTest(unittest.TestCase):
    def test_parser_worker_limit_serializes_work(self):
        limiter_name = f"test-{uuid.uuid4()}"
        active_workers = 0
        max_active_workers = 0
        state_lock = threading.Lock()
        start = threading.Barrier(3)

        def worker():
            nonlocal active_workers, max_active_workers
            start.wait()
            with parser_worker_limit(limiter_name, 1):
                with state_lock:
                    active_workers += 1
                    max_active_workers = max(max_active_workers, active_workers)
                time.sleep(0.02)
                with state_lock:
                    active_workers -= 1

        threads = [threading.Thread(target=worker) for _ in range(2)]
        for thread in threads:
            thread.start()

        start.wait()
        for thread in threads:
            thread.join()

        self.assertEqual(max_active_workers, 1)

    @unittest.skipUnless(CONFIG.pdfium_serialize, "pdfium serialization disabled")
    def test_pdfium_lock_serializes_and_is_reentrant(self):
        active = 0
        peak = 0
        state_lock = threading.Lock()
        start = threading.Barrier(5)  # 4 workers + main

        def worker():
            nonlocal active, peak
            start.wait()
            with pdfium_lock():
                with state_lock:
                    active += 1
                    peak = max(peak, active)
                time.sleep(0.02)
                with state_lock:
                    active -= 1

        threads = [threading.Thread(target=worker) for _ in range(4)]
        for thread in threads:
            thread.start()
        start.wait()
        for thread in threads:
            thread.join()

        self.assertEqual(peak, 1)
        # Reentrant: nested acquisition must not deadlock.
        with pdfium_lock():
            with pdfium_lock():
                pass

    @unittest.skipUnless(CONFIG.pdfium_serialize, "pdfium serialization disabled")
    def test_concurrent_pdf_parses_serialize_pdfium_access(self):
        """Concurrent PDF parses must never enter pdfium concurrently.

        Regression test for the pdfium thread-safety bug: before the fix the
        route called pdfium without any gate, so peak concurrency was > 1.
        """
        active = 0
        peak = 0
        calls = 0
        state_lock = threading.Lock()
        real_gate = pdfium_lock

        @contextlib.contextmanager
        def counting_pdfium_lock():
            nonlocal active, peak, calls
            # Delegate to the real gate so serialization is actually enforced,
            # then observe how many threads are inside the critical section.
            with real_gate():
                with state_lock:
                    calls += 1
                    active += 1
                    peak = max(peak, active)
                try:
                    yield
                finally:
                    with state_lock:
                        active -= 1

        pdf_bytes = _make_scanned_pdf(pages=2)
        errors: list = []

        def worker(i):
            try:
                PDFParser(
                    file_name=f"scan_{i}.pdf", file_type="pdf"
                ).parse_into_text(pdf_bytes)
            except Exception as exc:  # noqa: BLE001
                errors.append(exc)

        with mock.patch.object(pdf_parser, "pdfium_lock", counting_pdfium_lock):
            threads = [threading.Thread(target=worker, args=(i,)) for i in range(4)]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join()

        self.assertEqual(errors, [])
        self.assertGreater(calls, 0, "PDF route never acquired the pdfium gate")
        self.assertEqual(peak, 1)

    def test_scanned_pdf_parser_outputs_jpeg_images(self):
        pdf_bytes = io.BytesIO()
        pages = [
            Image.new("RGB", (64, 64), "white"),
            Image.new("RGB", (64, 64), "black"),
        ]
        pages[0].save(
            pdf_bytes,
            format="PDF",
            save_all=True,
            append_images=pages[1:],
        )

        document = PDFScannedParser(file_name="scan.pdf").parse_into_text(
            pdf_bytes.getvalue()
        )

        image_ref = "images/scan_page_1.jpg"
        self.assertIn(f"![scan_page_1.jpg]({image_ref})", document.content)
        self.assertIn(image_ref, document.images)
        self.assertEqual(document.metadata["image_source_type"], "scanned_pdf")
        self.assertEqual(document.metadata["page_count"], 2)
        self.assertEqual(len(document.images), 2)
        self.assertIn("images/scan_page_2.jpg", document.images)
        image_bytes = base64.b64decode(document.images[image_ref])
        self.assertTrue(image_bytes.startswith(b"\xff\xd8"))

    def test_scanned_pdf_parser_logs_malformed_pdf_without_format_error(self):
        parser = PDFScannedParser(file_name="broken.pdf")

        with self.assertLogs("docreader.parser.pdf_parser", level="ERROR") as logs:
            with self.assertRaises(Exception):
                parser.parse_into_text(b"not a pdf")

        self.assertTrue(
            any("PDFScannedParser failed to parse PDF:" in line for line in logs.output)
        )

    def test_normalize_image_quality_bounds_jpeg_quality(self):
        self.assertEqual(_normalize_image_quality(-1), 1)
        self.assertEqual(_normalize_image_quality(90), 90)
        self.assertEqual(_normalize_image_quality(120), 95)


if __name__ == "__main__":
    unittest.main()
