from setuptools import setup, find_packages

version = "0.1.0"

setup(
    name="harvester_api",
    version=version,
    author="Lanfon",
    author_email="lanfon.fan@suse.com",
    long_description="Unavailable",
    long_description_content_type="text/markdown",
    url="https://github.com/harvester/tests",
    packages=find_packages(),
    install_requires=["requests"],
    classifiers=[
        "Development Status :: 1 - Planning",
        "Operating System :: OS Independent",
        "Programming Language :: Python :: 3 :: Only"
    ],
    python_requires='>=3.6',
)
