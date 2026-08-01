const scopes = [
  'core',
  'agent',
  'contracts',
  'console',
  'deploy',
  'docs',
  'ci',
  'deps',
  'release',
  'security',
]

module.exports = {
  extends: ['@commitlint/config-conventional'],
  rules: {
    'scope-enum': [2, 'always', scopes],
    'subject-case': [0],
    'body-max-line-length': [2, 'always', 100],
    'footer-max-line-length': [2, 'always', 100],
  },
}
