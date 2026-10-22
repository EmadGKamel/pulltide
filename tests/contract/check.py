#!/usr/bin/env python3
"""Offline Milestone 01 checks; not the production Application validator."""

import copy
from dataclasses import dataclass
from importlib import metadata
import json
import math
from pathlib import Path
import re
import socket
import sys
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
SCHEMA_PATH = ROOT / 'schemas/application-v1alpha1.schema.json'
FIXTURES = ROOT / 'tests/fixtures/contract'


def require_tools():
    """Check every direct/transitive pin without installing or resolving anything."""
    for line in (ROOT / 'tests/contract/requirements.lock').read_text().splitlines():
        if not line or line.startswith('#'):
            continue
        name, expected = line.split('==')
        try:
            actual = metadata.version(name)
        except metadata.PackageNotFoundError:
            actual = 'missing'
        if actual != expected:
            raise RuntimeError(f'{name}: need {expected}, found {actual}; see README.md')


require_tools()
import yaml  # noqa: E402 -- dependency availability was checked above
from jsonschema import Draft202012Validator, RefResolver  # noqa: E402

SCHEMA = json.loads(SCHEMA_PATH.read_text())
CASES = json.loads((FIXTURES / 'cases.json').read_text())['cases']


@dataclass(frozen=True)
class Issue:
    stage: str
    code: str
    path: tuple = ()


class InputError(ValueError):
    def __init__(self, code):
        super().__init__(code)
        self.code = code


def load_document(text):
    """Preserve/check YAML information that JSON Schema cannot see."""
    events = list(yaml.parse(text, Loader=yaml.SafeLoader))
    if sum(isinstance(e, yaml.events.DocumentStartEvent) for e in events) != 1:
        raise InputError('document-count')
    if any(isinstance(e, yaml.events.AliasEvent) for e in events):
        raise InputError('alias')
    node = yaml.compose(text, Loader=yaml.SafeLoader)
    scalar_tags = {'str', 'int', 'float', 'bool', 'null'}

    def visit(current):
        short_tag = current.tag.removeprefix('tag:yaml.org,2002:')
        if not current.tag.startswith('tag:yaml.org,2002:'):
            raise InputError('custom-tag')
        if isinstance(current, yaml.MappingNode):
            if short_tag != 'map':
                raise InputError('yaml-type')
            seen = set()
            for key, value in current.value:
                if key.tag == 'tag:yaml.org,2002:merge':
                    raise InputError('merge-key')
                if key.tag != 'tag:yaml.org,2002:str':
                    raise InputError('mapping-key')
                if key.value in seen:
                    raise InputError('duplicate-key')
                seen.add(key.value)
                visit(key)
                visit(value)
        elif isinstance(current, yaml.SequenceNode):
            if short_tag != 'seq':
                raise InputError('yaml-type')
            for value in current.value:
                visit(value)
        elif short_tag not in scalar_tags:
            # Standard tags outside the contract's JSON-compatible value types.
            raise InputError('yaml-type')
        if isinstance(current, yaml.ScalarNode) and any(
            0xD800 <= ord(char) <= 0xDFFF for char in current.value
        ):
            raise InputError('unicode')

    visit(node)
    if isinstance(node, yaml.MappingNode):
        keys = [key.value for key, _ in node.value]
        if 'apiVersion' in keys and 'kind' in keys and keys[:2] != ['apiVersion', 'kind']:
            raise InputError('entry-order')
    data = yaml.safe_load(text)

    def json_values(value):
        if isinstance(value, float) and not math.isfinite(value):
            raise InputError('non-json-number')
        if isinstance(value, dict):
            for child in value.values():
                json_values(child)
        elif isinstance(value, list):
            for child in value:
                json_values(child)

    json_values(data)
    return data


def no_remote_reference(uri):
    raise AssertionError('Remote schema retrieval is forbidden')


VALIDATOR = Draft202012Validator(
    SCHEMA,
    resolver=RefResolver.from_schema(
        SCHEMA, handlers={'http': no_remote_reference, 'https': no_remote_reference}
    ),
)


def schema_issues(document):
    return [Issue('schema', str(error.validator), tuple(error.absolute_path))
            for error in VALIDATOR.iter_errors(document)]


def semantic_issues(document):
    """Only the two documented scalar/cross-field checks covered by this milestone.

    This is test-harness reference behavior, not default insertion, URI parsing,
    configuration hashing, repository preparation, or a production validator.
    Call only after structural validation succeeds.
    """
    spec = document['spec']
    fields = []
    for key in ('intervalSeconds', 'operationTimeoutSeconds'):
        if key in spec.get('reconcile', {}):
            fields.append((('spec', 'reconcile', key), spec['reconcile'][key]))
    health = spec['health']
    for key in ('timeoutSeconds', 'intervalSeconds'):
        if key in health:
            fields.append((('spec', 'health', key), health[key]))
    for index, check in enumerate(health['checks']):
        if 'timeoutSeconds' in check:
            fields.append((('spec', 'health', 'checks', index, 'timeoutSeconds'), check['timeoutSeconds']))
    errors = [Issue('semantic', 'yaml-integer', path)
              for path, value in fields if type(value) is not int]
    overall = health.get('timeoutSeconds', SCHEMA['$defs']['health']['properties']['timeoutSeconds']['default'])
    for index, check in enumerate(health['checks']):
        definition = 'httpCheck' if check['type'] == 'http' else 'commandCheck'
        timeout = check.get('timeoutSeconds', SCHEMA['$defs'][definition]['properties']['timeoutSeconds']['default'])
        if timeout > overall:
            errors.append(Issue('semantic', 'check-timeout', ('spec', 'health', 'checks', index, 'timeoutSeconds')))
    return errors


def inspect(text):
    try:
        document = load_document(text)
    except InputError as error:
        return None, [Issue('yaml', error.code)]
    except (yaml.YAMLError, ValueError, KeyError, OverflowError):
        return None, [Issue('yaml', 'syntax')]
    issues = schema_issues(document)
    if not issues:
        issues = semantic_issues(document)
    return document, issues


def at(document, path):
    for key in path:
        document = document[key]
    return document


class ContractChecks(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.base = load_document((FIXTURES / 'valid/exec-minimal.yaml').read_text())
        cls.compose = load_document((FIXTURES / 'valid/compose-ordered-files-checks.yaml').read_text())

    def assert_schema_rejects(self, document):
        # Assertions use only paths/codes, never raw user values or error messages.
        self.assertTrue(schema_issues(document), 'Expected a structural rejection')

    def test_meta_schema_and_local_references(self):
        Draft202012Validator.check_schema(SCHEMA)
        self.assertEqual(SCHEMA['$schema'], 'https://json-schema.org/draft/2020-12/schema')
        def visit(value):
            if isinstance(value, dict):
                if '$ref' in value:
                    self.assertTrue(value['$ref'].startswith('#/'), 'Remote reference')
                    at(SCHEMA, value['$ref'][2:].split('/'))
                self.assertNotIn('$data', value)
                self.assertNotIn('discriminator', value)
                for child in value.values():
                    visit(child)
            elif isinstance(value, list):
                for child in value:
                    visit(child)
        visit(SCHEMA)

    def test_fixture_manifest_is_complete(self):
        paths = [entry['file'] for entry in CASES]
        self.assertEqual(len(paths), len(set(paths)))
        actual = {str(p.relative_to(FIXTURES)) for p in FIXTURES.glob('*/*.yaml')}
        self.assertEqual(set(paths), actual)
        for entry in CASES:
            self.assertIn(entry['expect'], {'valid', 'schema', 'semantic', 'yaml'})

    def test_all_default_annotations_match_spec(self):
        actual = {}
        def walk(value, path=()):
            if isinstance(value, dict):
                if 'default' in value:
                    actual['/'.join(path)] = value['default']
                for key, child in value.items():
                    walk(child, path + (key,))
            elif isinstance(value, list):
                for index, child in enumerate(value):
                    walk(child, path + (str(index),))
        walk(SCHEMA)
        self.assertEqual(actual, {
            '$defs/gitSource/properties/path': '.',
            '$defs/spec/properties/reconcile': {},
            '$defs/reconcile/properties/intervalSeconds': 30,
            '$defs/reconcile/properties/operationTimeoutSeconds': 300,
            '$defs/exec/properties/env': {},
            '$defs/health/properties/timeoutSeconds': 60,
            '$defs/health/properties/intervalSeconds': 5,
            '$defs/httpCheck/properties/timeoutSeconds': 5,
            '$defs/commandCheck/properties/timeoutSeconds': 5,
            '$defs/spec/properties/rollback': {},
            '$defs/rollback/properties/enabled': True,
        })

    def test_validation_never_inserts_defaults(self):
        document = copy.deepcopy(self.base)
        original = copy.deepcopy(document)
        self.assertFalse(schema_issues(document))
        self.assertFalse(semantic_issues(document))
        self.assertEqual(document, original)
        self.assertNotIn('reconcile', document['spec'])
        self.assertNotIn('rollback', document['spec'])

    def test_every_required_field(self):
        required = [(), ('metadata',), ('spec',), ('spec', 'source'),
                    ('spec', 'runtime'), ('spec', 'runtime', 'exec'),
                    ('spec', 'health'), ('spec', 'health', 'checks', 0)]
        fields = [['apiVersion', 'kind', 'metadata', 'spec'], ['name'],
                  ['source', 'runtime', 'health'], ['type', 'url', 'ref'],
                  ['type', 'exec'], ['apply', 'stop'], ['checks'], ['type', 'url']]
        for parent, keys in zip(required, fields):
            for key in keys:
                with self.subTest(path=parent + (key,)):
                    document = copy.deepcopy(self.base)
                    del at(document, parent)[key]
                    self.assert_schema_rejects(document)
        for path in [('spec', 'runtime', 'docker-compose'), ('spec', 'runtime', 'docker-compose', 'files')]:
            document = copy.deepcopy(self.compose)
            del at(document, path[:-1])[path[-1]]
            self.assert_schema_rejects(document)
        document = copy.deepcopy(self.base)
        document['spec']['health']['checks'] = [{'type': 'command'}]
        self.assert_schema_rejects(document)

    def test_all_defined_objects_reject_unknown_fields(self):
        full = load_document((FIXTURES / 'valid/exec-explicit-defaults.yaml').read_text())
        for path in [(), ('metadata',), ('spec',), ('spec', 'source'), ('spec', 'reconcile'),
                     ('spec', 'runtime'), ('spec', 'runtime', 'exec'), ('spec', 'health'),
                     ('spec', 'health', 'checks', 0), ('spec', 'rollback')]:
            with self.subTest(path=path):
                document = copy.deepcopy(full)
                at(document, path)['unsupported'] = True
                self.assert_schema_rejects(document)
        document = copy.deepcopy(self.compose)
        document['spec']['runtime']['docker-compose']['unsupported'] = True
        self.assert_schema_rejects(document)
        document = copy.deepcopy(self.compose)
        document['spec']['health']['checks'][1]['unsupported'] = True
        self.assert_schema_rejects(document)

    def test_null_and_wrong_type_matrix(self):
        full = load_document((FIXTURES / 'valid/exec-explicit-defaults.yaml').read_text())
        typed_paths = {
            'string': [('apiVersion',), ('kind',), ('metadata', 'name'),
                       ('spec', 'source', 'type'), ('spec', 'source', 'url'),
                       ('spec', 'source', 'ref'), ('spec', 'source', 'path'),
                       ('spec', 'runtime', 'type'), ('spec', 'health', 'checks', 0, 'type'),
                       ('spec', 'health', 'checks', 0, 'url')],
            'object': [('metadata',), ('spec',), ('spec', 'source'), ('spec', 'reconcile'),
                       ('spec', 'runtime'), ('spec', 'runtime', 'exec'), ('spec', 'runtime', 'exec', 'env'),
                       ('spec', 'health'), ('spec', 'health', 'checks', 0), ('spec', 'rollback')],
            'array': [('spec', 'runtime', 'exec', 'apply'), ('spec', 'runtime', 'exec', 'stop'),
                      ('spec', 'health', 'checks')],
            'integer': [('spec', 'reconcile', 'intervalSeconds'), ('spec', 'reconcile', 'operationTimeoutSeconds'),
                        ('spec', 'health', 'timeoutSeconds'), ('spec', 'health', 'intervalSeconds'),
                        ('spec', 'health', 'checks', 0, 'timeoutSeconds')],
            'boolean': [('spec', 'rollback', 'enabled')],
        }
        bad = {'string': [None, True, 1, [], {}], 'object': [None, True, 1, [], 'x'],
               'array': [None, True, 1, {}, 'x'], 'integer': [None, True, 1.5, '5', [], {}],
               'boolean': [None, 'true', 1, [], {}]}
        for kind, paths in typed_paths.items():
            for path in paths:
                for index, value in enumerate(bad[kind]):
                    with self.subTest(path=path, case=index):
                        document = copy.deepcopy(full)
                        at(document, path[:-1])[path[-1]] = value
                        self.assert_schema_rejects(document)
        for path in [('spec', 'runtime', 'docker-compose'), ('spec', 'runtime', 'docker-compose', 'files'),
                     ('spec', 'health', 'checks', 1, 'command')]:
            document = copy.deepcopy(self.compose)
            at(document, path[:-1])[path[-1]] = None
            self.assert_schema_rejects(document)

    def test_runtime_and_check_member_types(self):
        for value in [None, True, 1, {}, 'compose.yaml', [], [None], [1], ['']]:
            document = copy.deepcopy(self.compose)
            document['spec']['runtime']['docker-compose']['files'] = value
            self.assert_schema_rejects(document)
        for command in [[], [''], [None], [1], ['./check.sh', None], ['./check.sh', 1]]:
            document = copy.deepcopy(self.base)
            document['spec']['health']['checks'] = [{'type': 'command', 'command': command}]
            self.assert_schema_rejects(document)
        for value in [None, True, 1, [], {}]:
            document = copy.deepcopy(self.base)
            document['spec']['runtime']['exec']['env'] = {'VALUE': value}
            self.assert_schema_rejects(document)
        document = copy.deepcopy(self.base)
        document['spec']['health']['checks'] = [{'type': 'command', 'command': ['./check.sh'], 'url': 'http://localhost'}]
        self.assert_schema_rejects(document)

    def test_normative_application_example(self):
        text = (ROOT / 'specs/001-application-contract.md').read_text()
        examples = [block for block in re.findall(r'```yaml\n(.*?)```', text, re.S)
                    if 'spec:' in block]
        self.assertEqual(len(examples), 1)
        _, issues = inspect(examples[0])
        self.assertFalse(issues, [(i.stage, i.code, i.path) for i in issues])

    def test_duration_boundaries_and_comparison(self):
        for overall, check in [(1, 1), (60, 5), (86400, 86400)]:
            document = copy.deepcopy(self.base)
            document['spec']['health']['timeoutSeconds'] = overall
            document['spec']['health']['checks'][0]['timeoutSeconds'] = check
            self.assertFalse(schema_issues(document))
            self.assertFalse(semantic_issues(document))
        for value in [0, -1, 86401, 1.5, True, '5', None]:
            document = copy.deepcopy(self.base)
            document['spec']['reconcile'] = {'operationTimeoutSeconds': value}
            self.assert_schema_rejects(document)
        document = copy.deepcopy(self.base)
        document['spec']['health']['timeoutSeconds'] = 4
        self.assertFalse(schema_issues(document))
        self.assertEqual(semantic_issues(document)[0].code, 'check-timeout')

    def test_meaningful_array_order_and_duplicates_preserved(self):
        text = (FIXTURES / 'valid/compose-ordered-files-checks.yaml').read_text()
        document, issues = inspect(text)
        self.assertFalse(issues)
        self.assertEqual(document['spec']['runtime']['docker-compose']['files'],
                         ['compose.yaml', 'compose.override.yaml', 'compose.yaml'])
        self.assertEqual([c['type'] for c in document['spec']['health']['checks']], ['http', 'command'])
        self.assertEqual(document['spec']['health']['checks'][1]['command'], ['./check.sh', 'literal argument', ''])

    def test_source_ref_format(self):
        good = ['refs/heads/main', 'refs/tags/v1', 'refs/heads/a./b',
                'refs/heads/مرحبا', 'refs/heads/a]b', 'refs/heads/@', 'A' * 40, 'a' * 64]
        bad = ['main', 'a' * 39, 'refs/remotes/origin/main', 'refs/heads/.hidden',
               'refs/heads/a/.b', 'refs/heads/a.lock/b', 'refs/heads/a//b',
               'refs/heads/a..b', 'refs/heads/a@{b', 'refs/heads/a.', 'refs/heads/a/',
               'refs/heads/a b', 'refs/heads/a\\b', 'refs/heads/a[b', 'refs/heads/a\n']
        for value in good + bad:
            document = copy.deepcopy(self.base)
            document['spec']['source']['ref'] = value
            with self.subTest(index=(good + bad).index(value)):
                self.assertEqual(not schema_issues(document), value in good)

    def test_relative_paths_are_linux_paths_not_shell_or_windows_rules(self):
        good = ['.', './app', 'a//b', 'a..b', '.hidden', 'a\\b', 'C:/repo', 'a/..\n']
        bad = ['', '/app', '..', '../app', 'a/../b', 'a/..', 'a/../../b']
        for value in good + bad:
            document = copy.deepcopy(self.base)
            document['spec']['source']['path'] = value
            with self.subTest(index=(good + bad).index(value)):
                self.assertEqual(not schema_issues(document), value in good)

    def test_name_and_env_key_full_string_matching(self):
        for name in ['a', 'a' * 63, 'a-b']:
            document = copy.deepcopy(self.base)
            document['metadata']['name'] = name
            self.assertFalse(schema_issues(document))
        for name in ['', 'a' * 64, '-a', 'a-', 'A', 'a\n']:
            document = copy.deepcopy(self.base)
            document['metadata']['name'] = name
            self.assert_schema_rejects(document)
        for key in ['_KEY', 'KEY2']:
            document = copy.deepcopy(self.base)
            document['spec']['runtime']['exec']['env'] = {key: ''}
            self.assertFalse(schema_issues(document))
        for key in ['KEY\n', '2KEY', 'BAD-KEY', '']:
            document = copy.deepcopy(self.base)
            document['spec']['runtime']['exec']['env'] = {key: ''}
            self.assert_schema_rejects(document)

    def test_application_does_not_accept_status_contract(self):
        for key, value in [('statusVersion', 'pulltide.io/status/v1alpha1'), ('active', {}), ('instanceId', 'example')]:
            document = copy.deepcopy(self.base)
            document[key] = value
            self.assert_schema_rejects(document)


def fixture_test(entry):
    def test(self):
        document, issues = inspect((FIXTURES / entry['file']).read_text())
        if entry['expect'] == 'valid':
            self.assertFalse(issues, [(i.stage, i.code, i.path) for i in issues])
            self.assertIsNotNone(document)
        else:
            self.assertTrue(issues, 'Expected rejection')
            self.assertEqual({i.stage for i in issues}, {entry['expect']})
            if 'code' in entry:
                self.assertIn(entry['code'], {i.code for i in issues})
    return test


for entry in CASES:
    name = 'test_fixture_' + re.sub(r'[^A-Za-z0-9_]', '_', entry['file'])
    setattr(ContractChecks, name, fixture_test(entry))


if __name__ == '__main__':
    print(f'Contract fixtures: {len(CASES)}; network disabled; exact dependency pins verified.', flush=True)
    # A future accidental remote reference or format implementation must fail, not fetch.
    with mock.patch.object(socket, 'socket', side_effect=AssertionError('Network disabled')):
        suite = unittest.defaultTestLoader.loadTestsFromTestCase(ContractChecks)
        result = unittest.TextTestRunner(verbosity=1).run(suite)
    sys.exit(not result.wasSuccessful())
