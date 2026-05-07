"""
Built-in Blocks Network handlers.

Exports:
    echo_handler  - Echo handler (returns input text back).
    adder_handler - Adder handler (adds two numbers).
"""

from .echo import echo_handler
from .adder import adder_handler

__all__ = [
    "echo_handler",
    "adder_handler",
]
