class Dolt < Formula
  VERSION = '0.9.7'

  desc "Dolt - It's git for data"
  homepage 'https://github.com/fanahova/dolt'
  url "https://github.com/fanahova/dolt/archive/v#{VERSION}.tar.gz"
  version VERSION
  sha256 '931d1ba53a54d8aef08c2f9f27e16160eea059a3dae1c95ba6ca6760ea7be794'

  depends_on 'go' => :build

  bottle :unneeded

  def install
    system "cd go && GOBIN=/usr/local/Cellar/dolt/#{VERSION} go install ./cmd/dolt"
    system "cd go && GOBIN=/usr/local/Cellar/dolt/#{VERSION} go install ./cmd/git-dolt"
    system "cd go && GOBIN=/usr/local/Cellar/dolt/#{VERSION} go install ./cmd/git-dolt-smudge"
  end

  test do
    system "#{bin}/dolt", '--version'
  end
end
