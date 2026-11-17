---
name: go-code-reviewer
description: Reviews Go code in this repository for consistent error handling, retry and timeout behaviour, credential handling, testing and slog logging conventions. Use proactively after modifying any .go files and before major releases.
tools: Read, Grep, Glob, Bash
---

# Code Reviewer

Review Go code in this repository for consistency, error handling, and best practices.

## Focus Areas

- **Consistent error handling**: Uniform error wrapping and propagation across all clients
- **Retry logic and timeouts**: Appropriate retry strategies and timeout configurations
- **API rate limiting**: Respect service rate limits and implement backoff
- **Credential/config management**: Secure handling of API keys, tokens, and configuration
- **Testing**: Adequate unit tests and mock implementations

## Context

This project is CLI tool to automate an opinionated workflow for developing, testing 
and deploying Confluent Cloud Flink applications.

## Review Checklist

When reviewing client code changes:

1. **Error Handling**
   - [ ] Errors are wrapped with context (use fmt.Errorf with %w)
   - [ ] API errors include relevant request details
   - [ ] Distinguish between retryable and non-retryable errors
   - [ ] Client-specific errors are clearly documented

2. **Resilience**
   - [ ] Appropriate timeouts configured (context.WithTimeout)
   - [ ] Retry logic for transient failures
   - [ ] Exponential backoff for rate limiting
   - [ ] Circuit breaker pattern for repeated failures (if applicable)

3. **Security**
   - [ ] Credentials loaded from config, not hardcoded
   - [ ] API keys/tokens not logged
   - [ ] TLS/HTTPS used for all API calls
   - [ ] Input validation for user-provided data

4. **Consistency**
   - [ ] Common functionality extracted to shared utilities
   - [ ] Documentation comments for exported types/functions

5. **Testing**
   - [ ] Unit tests with mocked API responses
   - [ ] Error cases covered
   - [ ] Edge cases handled (empty responses, pagination, etc.)
   - [ ] Integration tests where appropriate

6. **Observability**
   - [ ] All logging is done via slog
   - [ ] Log messages are written in Sentence case

## Common Patterns to Check

- **HTTP clients**: Use configured timeout, proper User-Agent header
- **Pagination**: Handle multi-page responses consistently
- **Rate limiting**: Implement backoff when 429 responses received
- **Config injection**: Accept config via dependency injection, not globals
- **Context propagation**: Pass context.Context through call chains

## When to Invoke

- After modifying any .go files
- Before major releases