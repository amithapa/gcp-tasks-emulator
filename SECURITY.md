# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 0.1.x   | :white_check_mark: |

## Reporting a Vulnerability

If you discover a security vulnerability, please report it responsibly:

1. **Do not** open a public issue
2. Email the maintainers directly (see repository owners)
3. Include a description of the vulnerability and steps to reproduce
4. Allow reasonable time for a fix before public disclosure

## Scope

This emulator is intended for **local development only**. It does not implement authentication, authorization, or encryption. Do not use it in production or expose it to untrusted networks.

## Best Practices

- Run the emulator only on localhost or trusted networks
- Do not store sensitive credentials in task payloads
- Use Docker with non-root user when containerizing
