"""Errors that the API turns into HTTP responses."""


class ApiError(Exception):
    """A failure the caller should see. `code` is stable, `message` is for people."""

    def __init__(self, status: int, code: str, message: str):
        super().__init__(message)
        self.status = status
        self.code = code
        self.message = message


def bad_request(code: str, message: str) -> ApiError:
    return ApiError(400, code, message)


def unauthorized(message: str = "Sign in to continue") -> ApiError:
    return ApiError(401, "unauthorized", message)


def forbidden(message: str = "You do not have access to this") -> ApiError:
    return ApiError(403, "forbidden", message)


def not_found(message: str = "Not found") -> ApiError:
    return ApiError(404, "not_found", message)


def conflict(code: str, message: str) -> ApiError:
    return ApiError(409, code, message)
