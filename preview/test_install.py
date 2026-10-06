"""Run with python3 -m unittest discover -s preview -p 'test_*.py'."""
import contextlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from install import install


class PreviewInstallTest(unittest.TestCase):
    def setUp(self):
        self.package = Path(__file__).resolve().parents[1]
        self.binary = self.package / 'build/preview/eigenflux'
        self.assertTrue(self.binary.is_file(), 'Build the pinned CLI as described in install.md first')
        self.temp = tempfile.TemporaryDirectory(prefix='ef-preview-')
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name).resolve()

    def run_install(self, root):
        with contextlib.redirect_stdout(io.StringIO()):
            install(self.package, root, self.binary)

    def test_nondefault_home_rerun_and_foreign_override(self):
        root = self.base / 'a path with spaces'
        foreign = self.base / 'foreign skills'
        foreign.mkdir()
        sentinel = foreign / 'sentinel'
        sentinel.write_text('untouched')
        with patch.dict('os.environ', {'EIGENFLUX_SKILLS_DIR': str(foreign)}):
            self.run_install(root)
            marker = root / '.eigenflux/progress-sentinel'
            marker.write_text('keep prior progress')
            self.run_install(root)
        self.assertEqual(marker.read_text(), 'keep prior progress')
        self.assertEqual(list(foreign.iterdir()), [sentinel])
        self.assertEqual(sentinel.read_text(), 'untouched')
        receipt = json.loads((root / 'preview-receipt.json').read_text())
        self.assertEqual(receipt['status'], 'ready')
        self.assertEqual(receipt['agent_home'], str(root / '.eigenflux'))
        self.assertEqual(receipt['skills_dir'], str(root / 'skills'))
        config = json.loads((root / '.eigenflux/config.json').read_text())
        self.assertEqual(config['kv']['auto_skill_sync'], 'false')
        for file in (self.package / 'skills').glob('ef-*/**/*'):
            if file.is_file():
                self.assertEqual(file.read_bytes(), (root / 'skills' / file.relative_to(self.package / 'skills')).read_bytes())

    def test_foreign_root_and_symlink_rejected(self):
        foreign = self.base / 'foreign'
        foreign.mkdir()
        (foreign / 'sentinel').write_text('keep')
        with self.assertRaises(ValueError):
            self.run_install(foreign)
        link = self.base / 'link'
        link.symlink_to(foreign, target_is_directory=True)
        with self.assertRaises(ValueError):
            self.run_install(link)
        self.assertEqual((foreign / 'sentinel').read_text(), 'keep')

    def test_different_source_receipt_rejected(self):
        root = self.base / 'owned'
        self.run_install(root)
        file = root / 'preview-receipt.json'
        receipt = json.loads(file.read_text())
        receipt['source_commit'] = 'another-source'
        file.write_text(json.dumps(receipt))
        with self.assertRaises(ValueError):
            self.run_install(root)
        self.assertEqual(json.loads(file.read_text())['source_commit'], 'another-source')


if __name__ == '__main__':
    unittest.main()
