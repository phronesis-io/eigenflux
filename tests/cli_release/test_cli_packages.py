"""Complete-package fixtures never invoke a notification app or Apple service."""
import importlib.util
from pathlib import Path
import tempfile
import unittest
import zipfile

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location('packages', ROOT / 'cli/scripts/cli-packages.py')
packages = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(packages)


class PackageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='complete CLI packages ')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.build = self.root / 'build'
        self.build.mkdir()
        self.helpers = self.root / 'helpers'
        for os_name, arch in packages.PLATFORMS:
            (self.build / packages.binary_name(os_name, arch)).write_bytes((os_name + arch + ' CLI').encode())
        for arch in ('amd64', 'arm64'):
            helper = self.helpers / f'darwin-{arch}' / packages.HELPER
            for relative in ('Contents/Info.plist', 'Contents/MacOS/EigenFluxNotifications', 'Contents/Resources/EigenFlux.icns'):
                target = helper / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes((arch + relative).encode())
                if '/MacOS/' in relative:
                    target.chmod(0o755)

    def test_packages_are_deterministic_and_pair_correct_cpu_helper(self):
        packages.generate(self.build, self.helpers)
        packages.verify(self.build)
        first = {name: (self.build / name).read_bytes() for name in packages.package_names()}
        packages.generate(self.build, self.helpers)
        self.assertEqual(first, {name: (self.build / name).read_bytes() for name in packages.package_names()})
        for arch in ('amd64', 'arm64'):
            with zipfile.ZipFile(self.build / f'eigenflux-darwin-{arch}.zip') as archive:
                self.assertEqual(archive.read('eigenflux'), ('darwin' + arch + ' CLI').encode())
                helper = packages.HELPER + '/Contents/MacOS/EigenFluxNotifications'
                self.assertTrue(archive.read(helper).startswith(arch.encode()))
                self.assertEqual((archive.getinfo(helper).external_attr >> 16) & 0o777, 0o755)
        with zipfile.ZipFile(self.build / 'eigenflux-windows-amd64.exe.zip') as archive:
            self.assertEqual(archive.namelist(), ['eigenflux.exe'])

    def test_missing_helper_and_symlink_are_rejected(self):
        target = self.helpers / 'darwin-amd64' / packages.HELPER / 'Contents/Info.plist'
        original = target.read_bytes()
        target.unlink()
        with self.assertRaisesRegex(ValueError, 'missing complete'):
            packages.generate(self.build, self.helpers)
        external = self.root / 'external'
        external.write_bytes(original)
        target.symlink_to(external)
        with self.assertRaisesRegex(ValueError, 'symlink'):
            packages.generate(self.build, self.helpers)

    def test_bare_binary_drift_and_incomplete_zip_are_rejected(self):
        packages.generate(self.build, self.helpers)
        (self.build / 'eigenflux-windows-amd64.exe').write_bytes(b'changed after packaging')
        with self.assertRaisesRegex(ValueError, 'differs from bare'):
            packages.verify(self.build)
        packages.generate(self.build, self.helpers)
        with zipfile.ZipFile(self.build / 'eigenflux-darwin-amd64.zip', 'w') as archive:
            archive.writestr('eigenflux', b'bad')
        with self.assertRaises(ValueError):
            packages.verify(self.build)


if __name__ == '__main__':
    unittest.main()
